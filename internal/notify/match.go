package notify

import (
	"strings"
	"time"
)

type messageContext struct {
	AccountPublicID  string
	Groups           []string
	Tags             []string
	Category         string
	SenderAddress    string
	Subject          string
	Body             string
	VerificationCode string
	Personal         bool
	ReceivedAt       time.Time
}

func ruleMatches(rule Rule, message messageContext, location *time.Location) bool {
	if !rule.Enabled {
		return false
	}
	if rule.PersonalOnly && !message.Personal {
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
	if rule.SenderAddress != "" && !strings.EqualFold(strings.TrimSpace(rule.SenderAddress), strings.TrimSpace(message.SenderAddress)) {
		return false
	}
	if rule.SenderDomain != "" {
		_, domain, ok := strings.Cut(message.SenderAddress, "@")
		if !ok || !strings.EqualFold(strings.TrimSpace(rule.SenderDomain), strings.TrimSpace(domain)) {
			return false
		}
	}
	if len(rule.SubjectKeywords) > 0 {
		subject := strings.ToLower(message.Subject)
		matched := false
		for _, keyword := range rule.SubjectKeywords {
			if strings.Contains(subject, strings.ToLower(strings.TrimSpace(keyword))) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if rule.RequireOTP && message.VerificationCode == "" {
		return false
	}
	if rule.StartMinute >= 0 && rule.EndMinute >= 0 {
		if location == nil {
			location = time.UTC
		}
		local := message.ReceivedAt.In(location)
		minute := local.Hour()*60 + local.Minute()
		if rule.StartMinute <= rule.EndMinute {
			if minute < rule.StartMinute || minute > rule.EndMinute {
				return false
			}
		} else if minute < rule.StartMinute && minute > rule.EndMinute {
			return false
		}
	}
	return true
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func equalTrimmedFold(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func containsSubjectKeyword(keywords []string, subject string) bool {
	subject = strings.ToLower(subject)
	for _, keyword := range keywords {
		if strings.Contains(subject, strings.ToLower(strings.TrimSpace(keyword))) {
			return true
		}
	}
	return false
}

func intersectsFold(left, right []string) bool {
	for _, value := range left {
		if containsFold(right, value) {
			return true
		}
	}
	return false
}
