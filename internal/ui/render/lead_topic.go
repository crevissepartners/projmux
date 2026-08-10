package render

import "strings"

func styleLeadTopicPrefix(topic string) string {
	prefix, rest, ok := splitLeadTopicPrefix(topic)
	if !ok {
		return topic
	}
	return ansiBold + ansiProgress + prefix + ansiReset + rest
}

func splitLeadTopicPrefix(topic string) (string, string, bool) {
	lower := strings.ToLower(topic)
	for _, prefix := range []string{
		"[lead:qa]",
		"[lead:poc]",
		"[lead:ship]",
		"[lead:roadmap]",
	} {
		if strings.HasPrefix(lower, prefix) {
			return topic[:len(prefix)], topic[len(prefix):], true
		}
	}
	return "", "", false
}
