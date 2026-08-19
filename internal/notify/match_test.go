package notify

import (
	"testing"
	"time"
)

func TestRuleMatchesAllConfiguredConditions(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	rule := Rule{
		Enabled: true, AccountPublicIDs: []string{"acc_1"}, GroupNames: []string{"业务"},
		TagNames: []string{"重点"}, Categories: []string{"verification"}, SenderDomain: "example.com",
		SubjectKeywords: []string{"login"}, StartMinute: 8 * 60, EndMinute: 18 * 60,
		RequireOTP: true,
	}
	message := messageContext{
		AccountPublicID: "acc_1", Groups: []string{"业务"}, Tags: []string{"重点"}, Category: "verification",
		SenderAddress: "security@example.com", Subject: "Login code", VerificationCode: "123456",
		ReceivedAt: time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC),
	}
	if !ruleMatches(rule, message, location) {
		t.Fatal("rule should match")
	}
	message.VerificationCode = ""
	if ruleMatches(rule, message, location) {
		t.Fatal("rule requiring OTP must reject messages without a code")
	}
}

func TestRuleMatchesOvernightWindow(t *testing.T) {
	rule := Rule{Enabled: true, StartMinute: 22 * 60, EndMinute: 6 * 60}
	message := messageContext{ReceivedAt: time.Date(2026, 8, 17, 23, 30, 0, 0, time.UTC)}
	if !ruleMatches(rule, message, time.UTC) {
		t.Fatal("overnight window should include 23:30")
	}
	message.ReceivedAt = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	if ruleMatches(rule, message, time.UTC) {
		t.Fatal("overnight window should exclude noon")
	}
}
