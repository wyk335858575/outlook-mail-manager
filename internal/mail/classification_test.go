package mail

import (
	"context"
	"testing"
)

func TestClassifyMessageProtectsVerificationAndSpam(t *testing.T) {
	marketing := CategoryMarketing
	rules := []ClassificationRule{{
		Name: "可信发件人", MatchField: "domain", MatchOperator: "equals", MatchValue: "example.com",
		TargetCategory: &marketing, ProtectsCleanup: true, Priority: 500, Enabled: true,
	}}
	result := classifyMessage(ClassificationInput{
		Subject: "Your verification code is 482913", SenderAddress: "login@example.com", Folder: "inbox",
	}, rules)
	if result.Category != CategoryMarketing || !result.Protected || result.VerificationCode != "482913" {
		t.Fatalf("classification = %#v", result)
	}
	eligibility := cleanupEligibilityFor(ClassificationInput{}, result)
	if eligibility.Eligible {
		t.Fatal("protected message must not be cleanup eligible")
	}

	spam := classifyMessage(ClassificationInput{Folder: "junkemail"}, nil)
	if spam.Category != CategorySpam {
		t.Fatalf("spam category = %q", spam.Category)
	}
}

func TestCleanupEligibilityProtectsEveryNonCleanupCategory(t *testing.T) {
	protected := []Category{CategoryImportant, CategoryVerification, CategoryNormal, CategoryUncertain}
	for _, category := range protected {
		if cleanupEligibilityFor(ClassificationInput{}, ClassificationResult{Category: category}).Eligible {
			t.Fatalf("category %q must not be cleanup eligible", category)
		}
	}
	for _, category := range []Category{CategoryMarketing, CategorySpam} {
		if !cleanupEligibilityFor(ClassificationInput{}, ClassificationResult{Category: category}).Eligible {
			t.Fatalf("category %q should be cleanup eligible", category)
		}
	}
	if cleanupEligibilityFor(ClassificationInput{Flagged: true}, ClassificationResult{Category: CategorySpam}).Eligible {
		t.Fatal("flagged message must not be cleanup eligible")
	}
	if cleanupEligibilityFor(ClassificationInput{AccountLocked: true}, ClassificationResult{Category: CategoryMarketing}).Eligible {
		t.Fatal("protected account must not be cleanup eligible")
	}
}

func TestExtractVerificationCodeRequiresContext(t *testing.T) {
	if code := extractVerificationCode("Your login code: 708144"); code != "708144" {
		t.Fatalf("code = %q", code)
	}
	if code := extractVerificationCode("Order 708144 has shipped"); code != "" {
		t.Fatalf("unexpected code = %q", code)
	}
}

func TestVerificationSubjectWinsOverJunkFolder(t *testing.T) {
	result := classifyMessage(ClassificationInput{
		Subject: "RebatesMe verification code",
		Folder:  "junkemail",
	}, nil)
	if result.Category != CategoryVerification || result.Reason != "检测到验证码主题" {
		t.Fatalf("classification = %#v", result)
	}
}

func TestVerificationCodeRemainsCleanupProtectedWhenRuleChangesCategory(t *testing.T) {
	spam := CategorySpam
	result := classifyMessage(ClassificationInput{
		Subject: "Your verification code is 482913",
		Folder:  "junkemail",
	}, []ClassificationRule{{
		Name: "测试规则", MatchField: "subject", MatchOperator: "contains", MatchValue: "verification",
		TargetCategory: &spam, Priority: 500, Enabled: true,
	}})
	if !result.Protected || result.VerificationCode != "482913" {
		t.Fatalf("classification = %#v", result)
	}
	if cleanupEligibilityFor(ClassificationInput{}, result).Eligible {
		t.Fatal("message containing a verification code must not be cleanup eligible")
	}
}

func TestReclassifyVerificationDismissesExistingCleanupCandidate(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := formatTime(service.now())
	result, err := store.DB.Exec(`
		INSERT INTO messages (
			public_id, account_id, folder_id, immutable_id, subject, received_at_utc,
			category, classification_reason, classification_source, created_at_utc, updated_at_utc
		) VALUES ('msg_reclassified_verification', ?, ?, 'immutable-reclassified-code',
			'RebatesMe verification code', ?, 'spam', '来自 Microsoft 垃圾邮件文件夹', 'builtin', ?, ?)
	`, accountID, folders[1].id, now, now, now)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	messageID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("message ID: %v", err)
	}
	if _, err := store.DB.Exec(`
		INSERT INTO cleanup_actions (
			public_id, message_id, state, candidate_reason, created_at_utc, updated_at_utc
		) VALUES ('clean_reclassified_verification', ?, 'candidate', '来自 Microsoft 垃圾邮件文件夹', ?, ?)
	`, messageID, now, now); err != nil {
		t.Fatalf("insert cleanup candidate: %v", err)
	}

	if err := service.ReclassifyAll(context.Background()); err != nil {
		t.Fatalf("ReclassifyAll() error = %v", err)
	}
	var category, source, state string
	if err := store.DB.QueryRow(`
		SELECT m.category, m.classification_source, c.state
		FROM messages m JOIN cleanup_actions c ON c.message_id = m.id
		WHERE m.public_id = 'msg_reclassified_verification'
	`).Scan(&category, &source, &state); err != nil {
		t.Fatalf("load reclassified message: %v", err)
	}
	if category != "verification" || source != "builtin" || state != "dismissed" {
		t.Fatalf("category = %q, source = %q, cleanup state = %q", category, source, state)
	}
}
