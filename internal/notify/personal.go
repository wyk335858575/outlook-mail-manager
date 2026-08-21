package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type personalInboxRule struct {
	Enabled          bool
	AccountPublicIDs []string
	GroupNames       []string
	TagNames         []string
	Categories       []string
	SenderAddress    string
	SenderDomain     string
	SubjectKeywords  []string
	RequireOTP       bool
}

func (s *Service) isPersonalMessage(ctx context.Context, message messageContext) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT enabled, account_public_ids_json, group_names_json, tag_names_json,
			categories_json, sender_address, sender_domain, subject_keywords_json, require_otp
		FROM personal_inbox_rules WHERE enabled = 1 ORDER BY id
	`)
	if err != nil {
		return false, fmt.Errorf("list personal inbox rules for notification: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rule personalInboxRule
		var accountsJSON, groupsJSON, tagsJSON, categoriesJSON, keywordsJSON string
		if err := rows.Scan(&rule.Enabled, &accountsJSON, &groupsJSON, &tagsJSON, &categoriesJSON,
			&rule.SenderAddress, &rule.SenderDomain, &keywordsJSON, &rule.RequireOTP); err != nil {
			return false, fmt.Errorf("scan personal inbox rule for notification: %w", err)
		}
		for _, value := range []struct {
			data string
			dest *[]string
		}{
			{accountsJSON, &rule.AccountPublicIDs}, {groupsJSON, &rule.GroupNames},
			{tagsJSON, &rule.TagNames}, {categoriesJSON, &rule.Categories},
			{keywordsJSON, &rule.SubjectKeywords},
		} {
			if err := json.Unmarshal([]byte(value.data), value.dest); err != nil {
				return false, fmt.Errorf("decode personal inbox rule for notification: %w", err)
			}
		}
		if personalRuleMatches(rule, message) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func personalRuleMatches(rule personalInboxRule, message messageContext) bool {
	if !rule.Enabled {
		return false
	}
	if len(rule.AccountPublicIDs) > 0 && !containsFold(rule.AccountPublicIDs, message.AccountPublicID) {
		return false
	}
	if len(rule.GroupNames) > 0 && !intersectsFold(rule.GroupNames, message.Groups) {
		return false
	}
	if len(rule.TagNames) > 0 && !intersectsFold(rule.TagNames, message.Tags) {
		return false
	}
	if len(rule.Categories) > 0 && !containsFold(rule.Categories, message.Category) {
		return false
	}
	if rule.SenderAddress != "" && !equalTrimmedFold(rule.SenderAddress, message.SenderAddress) {
		return false
	}
	if rule.SenderDomain != "" {
		_, domain, ok := strings.Cut(message.SenderAddress, "@")
		if !ok || !equalTrimmedFold(rule.SenderDomain, domain) {
			return false
		}
	}
	if len(rule.SubjectKeywords) > 0 && !containsSubjectKeyword(rule.SubjectKeywords, message.Subject) {
		return false
	}
	return !rule.RequireOTP || message.VerificationCode != ""
}
