package aws

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/vault/sdk/logical"
)

func TestBackend_PathListRoles(t *testing.T) {
	var resp *logical.Response
	var err error
	config := logical.TestBackendConfig()
	config.StorageView = &logical.InmemStorage{}

	b := Backend()
	if err := b.Setup(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	roleData := map[string]interface{}{
		"role_arns":       []string{"arn:aws:iam::123456789012:role/path/RoleName"},
		"credential_type": assumedRoleCred,
		"default_sts_ttl": 3600,
		"max_sts_ttl":     3600,
	}

	roleReq := &logical.Request{
		Operation: logical.UpdateOperation,
		Storage:   config.StorageView,
		Data:      roleData,
	}

	for i := 1; i <= 10; i++ {
		roleReq.Path = "roles/testrole" + strconv.Itoa(i)
		resp, err = b.HandleRequest(context.Background(), roleReq)
		if err != nil || (resp != nil && resp.IsError()) {
			t.Fatalf("bad: role creation failed. resp:%#v\n err:%v", resp, err)
		}
	}

	resp, err = b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.ListOperation,
		Path:      "roles",
		Storage:   config.StorageView,
	})
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("bad: listing roles failed. resp:%#v\n err:%v", resp, err)
	}

	if len(resp.Data["keys"].([]string)) != 10 {
		t.Fatalf("failed to list all 10 roles")
	}

	resp, err = b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.ListOperation,
		Path:      "roles/",
		Storage:   config.StorageView,
	})
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("bad: listing roles failed. resp:%#v\n err:%v", resp, err)
	}

	if len(resp.Data["keys"].([]string)) != 10 {
		t.Fatalf("failed to list all 10 roles")
	}
}

func TestUpgradeLegacyPolicyEntry(t *testing.T) {
	var input string
	var expected awsRoleEntry
	var output *awsRoleEntry

	input = "arn:aws:iam::123456789012:role/path/RoleName"
	expected = awsRoleEntry{
		CredentialTypes:          []string{assumedRoleCred},
		RoleArns:                 []string{input},
		ProhibitFlexibleCredPath: true,
		Version:                  1,
	}
	output = upgradeLegacyPolicyEntry(input)
	if output.InvalidData != "" {
		t.Fatalf("bad: error processing upgrade of %q: got invalid data of %v", input, output.InvalidData)
	}
	if !reflect.DeepEqual(*output, expected) {
		t.Fatalf("bad: expected %#v; received %#v", expected, *output)
	}

	input = "arn:aws:iam::123456789012:policy/MyPolicy"
	expected = awsRoleEntry{
		CredentialTypes:          []string{iamUserCred},
		PolicyArns:               []string{input},
		ProhibitFlexibleCredPath: true,
		Version:                  1,
	}
	output = upgradeLegacyPolicyEntry(input)
	if output.InvalidData != "" {
		t.Fatalf("bad: error processing upgrade of %q: got invalid data of %v", input, output.InvalidData)
	}
	if !reflect.DeepEqual(*output, expected) {
		t.Fatalf("bad: expected %#v; received %#v", expected, *output)
	}

	input = "arn:aws:iam::aws:policy/AWSManagedPolicy"
	expected.PolicyArns = []string{input}
	output = upgradeLegacyPolicyEntry(input)
	if output.InvalidData != "" {
		t.Fatalf("bad: error processing upgrade of %q: got invalid data of %v", input, output.InvalidData)
	}
	if !reflect.DeepEqual(*output, expected) {
		t.Fatalf("bad: expected %#v; received %#v", expected, *output)
	}

	input = `
{
	"Version": "2012-10-07",
	"Statement": [
		{
			"Effect": "Allow",
			"Action": "ec2:Describe*",
			"Resource": "*"
		}
	]
}`
	compacted, err := compactJSON(input)
	if err != nil {
		t.Fatalf("error parsing JSON: %v", err)
	}
	expected = awsRoleEntry{
		CredentialTypes:          []string{iamUserCred, federationTokenCred},
		PolicyDocument:           compacted,
		ProhibitFlexibleCredPath: true,
		Version:                  1,
	}
	output = upgradeLegacyPolicyEntry(input)
	if output.InvalidData != "" {
		t.Fatalf("bad: error processing upgrade of %q: got invalid data of %v", input, output.InvalidData)
	}
	if !reflect.DeepEqual(*output, expected) {
		t.Fatalf("bad: expected %#v; received %#v", expected, *output)
	}

	// Due to lack of prior input validation, this could exist in the storage, and we need
	// to be able to read it out in some fashion, so have to handle this in a poor fashion
	input = "arn:gobbledygook"
	expected = awsRoleEntry{
		InvalidData: input,
		Version:     1,
	}
	output = upgradeLegacyPolicyEntry(input)
	if !reflect.DeepEqual(*output, expected) {
		t.Fatalf("bad: expected %#v; received %#v", expected, *output)
	}
}

func TestUserPathValidity(t *testing.T) {

	testCases := []struct {
		description string
		userPath    string
		isValid     bool
	}{
		{
			description: "Default",
			userPath:    "/",
			isValid:     true,
		},
		{
			description: "Empty",
			userPath:    "",
			isValid:     false,
		},
		{
			description: "Valid",
			userPath:    "/path/",
			isValid:     true,
		},
		{
			description: "Missing leading slash",
			userPath:    "path/",
			isValid:     false,
		},
		{
			description: "Missing trailing slash",
			userPath:    "/path",
			isValid:     false,
		},
		{
			description: "Invalid character",
			userPath:    "/šiauliai/",
			isValid:     false,
		},
		{
			description: "Max length",
			userPath:    "/" + strings.Repeat("a", 510) + "/",
			isValid:     true,
		},
		{
			description: "Too long",
			userPath:    "/" + strings.Repeat("a", 511) + "/",
			isValid:     false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			if tc.isValid != userPathRegex.MatchString(tc.userPath) {
				t.Fatalf("bad: expected %s", strconv.FormatBool(tc.isValid))
			}
		})
	}
}

func TestRoleEntryValidationCredTypes(t *testing.T) {
	roleEntry := awsRoleEntry{
		CredentialTypes: []string{},
		PolicyArns: []string{"arn:aws:iam::aws:policy/AdministratorAccess"},
	}
	if roleEntry.validate() == nil {
		t.Errorf("bad: invalid roleEntry with no CredentialTypes %#v passed validation", roleEntry)
	}
	roleEntry.CredentialTypes = []string{"invalid_type"}
	if roleEntry.validate() == nil {
		t.Errorf("bad: invalid roleEntry with invalid CredentialTypes %#v passed validation", roleEntry)
	}
	roleEntry.CredentialTypes = []string{iamUserCred, "invalid_type"}
	if roleEntry.validate() == nil {
		t.Errorf("bad: invalid roleEntry with invalid CredentialTypes %#v passed validation", roleEntry)
	}
}

func TestRoleEntryValidationIamUserCred(t *testing.T) {
	var allowAllPolicyDocument = `{"Version": "2012-10-17", "Statement": [{"Sid": "AllowAll", "Effect": "Allow", "Action": "*", "Resource": "*"}]}`

	roleEntry := awsRoleEntry{
		CredentialTypes: []string{iamUserCred},
		PolicyArns: []string{"arn:aws:iam::aws:policy/AdministratorAccess"},
	}
	err := roleEntry.validate()
	if err != nil {
		t.Errorf("bad: valid roleEntry %#v failed validation: %v", roleEntry, err)
	}
	roleEntry.PolicyDocument = allowAllPolicyDocument
	err = roleEntry.validate()
	if err != nil {
		t.Errorf("bad: valid roleEntry %#v failed validation: %v", roleEntry, err)
	}
	roleEntry.PolicyArns = []string{}
	err = roleEntry.validate()
	if err != nil {
		t.Errorf("bad: valid roleEntry %#v failed validation: %v", roleEntry, err)
	}

	roleEntry = awsRoleEntry{
		CredentialTypes: []string{iamUserCred},
		RoleArns: []string{"arn:aws:iam::123456789012:role/SomeRole"},
	}
	if roleEntry.validate() == nil {
		t.Errorf("bad: invalid roleEntry with invalid RoleArns parameter %#v passed validation", roleEntry)
	}

	roleEntry = awsRoleEntry{
		CredentialTypes: []string{iamUserCred},
		PolicyArns: []string{"arn:aws:iam::aws:policy/AdministratorAccess"},
		DefaultSTSTTL: 1,
	}
	if roleEntry.validate() == nil {
		t.Errorf("bad: invalid roleEntry with unrecognized DefaultSTSTTL %#v passed validation", roleEntry)
	}
	roleEntry.DefaultSTSTTL = 0
	roleEntry.MaxSTSTTL = 1
	if roleEntry.validate() == nil {
		t.Errorf("bad: invalid roleEntry with unrecognized MaxSTSTTL %#v passed validation", roleEntry)
	}
}

func TestRoleEntryValidationAssumedRoleCred(t *testing.T) {
	var allowAllPolicyDocument = `{"Version": "2012-10-17", "Statement": [{"Sid": "AllowAll", "Effect": "Allow", "Action": "*", "Resource": "*"}]}`
	roleEntry := awsRoleEntry{
		CredentialTypes: []string{assumedRoleCred},
		RoleArns: []string{"arn:aws:iam::123456789012:role/SomeRole"},
		PolicyDocument: allowAllPolicyDocument,
		DefaultSTSTTL: 2,
		MaxSTSTTL: 3,
	}
	if err := roleEntry.validate(); err != nil {
		t.Errorf("bad: valid roleEntry %#v failed validation: %v", roleEntry, err)
	}

	roleEntry.PolicyArns = []string{"arn:aws:iam::aws:policy/AdministratorAccess"}
	if roleEntry.validate() == nil {
		t.Errorf("bad: invalid roleEntry with unrecognized PolicyArns %#v passed validation", roleEntry)
	}
	roleEntry.PolicyArns = []string{}
	roleEntry.MaxSTSTTL = 1
	if roleEntry.validate() == nil {
		t.Errorf("bad: invalid roleEntry with MaxSTSTTL < DefaultSTSTTL %#v passed validation", roleEntry)
	}
	roleEntry.MaxSTSTTL = 0
	roleEntry.UserPath = "/foobar/"
	if roleEntry.validate() == nil {
		t.Errorf("bad: invalid roleEntry with unrecognized UserPath %#v passed validation", roleEntry)
	}
}

func TestRoleEntryValidationFederationTokenCred(t *testing.T) {
	var allowAllPolicyDocument = `{"Version": "2012-10-17", "Statement": [{"Sid": "AllowAll", "Effect": "Allow", "Action": "*", "Resource": "*"}]}`
	roleEntry := awsRoleEntry{
		CredentialTypes: []string{federationTokenCred},
		PolicyDocument: allowAllPolicyDocument,
		DefaultSTSTTL: 2,
		MaxSTSTTL: 3,
	}
	if err := roleEntry.validate(); err != nil {
		t.Errorf("bad: valid roleEntry %#v failed validation: %v", roleEntry, err)
	}

	roleEntry.RoleArns = []string{"arn:aws:iam::123456789012:role/SomeRole"}
	if roleEntry.validate() == nil {
		t.Errorf("bad: invalid roleEntry with unrecognized RoleArns %#v passed validation", roleEntry)
	}
	roleEntry.RoleArns = []string{}
	roleEntry.UserPath = "/foobar/"
	if roleEntry.validate() == nil {
		t.Errorf("bad: invalid roleEntry with unrecognized UserPath %#v passed validation", roleEntry)
	}

	roleEntry.UserPath = ""
	roleEntry.MaxSTSTTL = 1
	if roleEntry.validate() == nil {
		t.Errorf("bad: invalid roleEntry with MaxSTSTTL < DefaultSTSTTL %#v passed validation", roleEntry)
	}
}
