package mail

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestPersonalInboxRuleCRUD(t *testing.T) {
	service, store, _, _ := newSyncTestService(t)
	defer store.Close()
	service.random = bytes.NewReader(bytes.Join([][]byte{
		bytes.Repeat([]byte{1}, 36), bytes.Repeat([]byte{2}, 18),
		bytes.Repeat([]byte{3}, 18), bytes.Repeat([]byte{4}, 18),
	}, nil))

	created, err := service.CreatePersonalInboxRule(context.Background(), PersonalInboxRule{
		Name:            " PayPal 付款 ",
		Enabled:         true,
		Categories:      []string{"important", "important"},
		SenderDomain:    " paypal.com ",
		SubjectKeywords: []string{"payment", "rebate"},
	})
	if err != nil {
		t.Fatalf("CreatePersonalInboxRule() error = %v", err)
	}
	if created.PublicID == "" || created.Name != "PayPal 付款" || created.SenderDomain != "paypal.com" || len(created.Categories) != 1 {
		t.Fatalf("created rule = %#v", created)
	}

	created.Enabled = false
	created.RequireOTP = true
	updated, err := service.UpdatePersonalInboxRule(context.Background(), created.PublicID, created)
	if err != nil {
		t.Fatalf("UpdatePersonalInboxRule() error = %v", err)
	}
	if updated.Enabled || !updated.RequireOTP {
		t.Fatalf("updated rule = %#v", updated)
	}

	items, err := service.ListPersonalInboxRules(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("ListPersonalInboxRules() = %#v, %v", items, err)
	}
	if err := service.DeletePersonalInboxRule(context.Background(), created.PublicID); err != nil {
		t.Fatalf("DeletePersonalInboxRule() error = %v", err)
	}
	if _, err := service.getPersonalInboxRule(context.Background(), created.PublicID); !errors.Is(err, ErrPersonalRuleNotFound) {
		t.Fatalf("get deleted rule error = %v", err)
	}

	var audits int
	if err := store.DB.QueryRow(`
		SELECT COUNT(*) FROM audit_events
		WHERE entity_type = 'personal_inbox_rule' AND entity_public_id = ?
	`, created.PublicID).Scan(&audits); err != nil {
		t.Fatalf("count personal rule audits: %v", err)
	}
	if audits != 3 {
		t.Fatalf("personal rule audit count = %d, want 3", audits)
	}
}

func TestPersonalInboxRuleRequiresCondition(t *testing.T) {
	service, store, _, _ := newSyncTestService(t)
	defer store.Close()

	_, err := service.CreatePersonalInboxRule(context.Background(), PersonalInboxRule{Name: "Everything", Enabled: true})
	if !errors.Is(err, ErrInvalidPersonalRule) {
		t.Fatalf("empty condition error = %v", err)
	}
	_, err = service.CreatePersonalInboxRule(context.Background(), PersonalInboxRule{
		Name: "Invalid category", Enabled: true, Categories: []string{"other"},
	})
	if !errors.Is(err, ErrInvalidPersonalRule) {
		t.Fatalf("invalid category error = %v", err)
	}
}
