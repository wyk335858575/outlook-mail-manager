package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"outlook-mail-manager/internal/database"
	"outlook-mail-manager/internal/datakey"
)

func TestListRulesKeepsEveryEmptyConditionAsArray(t *testing.T) {
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()
	keyring := datakey.New(nil)
	if err := keyring.Unlock(make([]byte, 32)); err != nil {
		t.Fatalf("unlock test data key: %v", err)
	}
	service, err := New(store.DB, keyring, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer service.Close()
	channel, err := service.CreateChannel(context.Background(), ChannelInput{
		Name: "WXPush", Kind: "wxpush", Enabled: true,
		WXPushAppID: "app-id", WXPushAppSecret: "app-secret", WXPushUserID: "open-id", WXPushTemplateID: "template-id",
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	rule, err := service.CreateRule(context.Background(), Rule{
		ChannelPublicID: channel.PublicID,
		Name:            "Empty conditions",
		Enabled:         true,
		PersonalInbox:   true,
		PersonalOnly:    true,
		StartMinute:     -1,
		EndMinute:       -1,
	})
	if err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	if rule.AccountPublicIDs == nil || rule.GroupNames == nil || rule.TagNames == nil || rule.Categories == nil || rule.SubjectKeywords == nil {
		t.Fatalf("empty rule conditions contain nil slices: %#v", rule)
	}
	if rule.PersonalInbox {
		t.Fatal("legacy personal_inbox field must be normalized to false")
	}
	if !rule.PersonalOnly {
		t.Fatal("personal_only field was not persisted")
	}
	encoded, err := json.Marshal(rule)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, field := range []string{"account_public_ids", "group_names", "tag_names", "categories", "subject_keywords"} {
		if strings.Contains(string(encoded), `"`+field+`":null`) {
			t.Fatalf("%s serialized as null: %s", field, encoded)
		}
	}
}

