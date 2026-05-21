package i18n

var defaultCatalogData = map[Locale]map[Key]Entry{
	FallbackLocale: {
		KeyNotifyAIResponseComplete:      textEntry("Response complete"),
		KeyNotifyAIApprovalRequired:      textEntry("Approval required"),
		KeyNotifyAIInputRequired:         textEntry("Input required"),
		KeyNotifyAIError:                 textEntry("Error"),
		KeyNotifyAISubagentStopped:       textEntry("Subagent stopped"),
		KeyNotifyAITeammateWaiting:       textEntry("Teammate waiting"),
		KeyNotifyAIReviewPending:         textEntry("Review pending:"),
		KeyNotifyQueueRowAgeCompact:      textEntry("{age} ago"),
		KeyNotifyQueueActionAck:          textEntry("ack"),
		KeyStatusNotifyCount:             textEntry("{count} notifications"),
		KeyStatusNotifyAgeCompact:        textEntry("{age} ago"),
		KeySettingsRootNotifications:     textEntry("Notifications"),
		KeySettingsRootProject:           textEntry("Project"),
		KeySettingsRootAppearance:        textEntry("Appearance"),
		KeySettingsNotificationsDesktop:  textEntry("Desktop notifications"),
		KeySettingsNotificationsDelivery: textEntry("Delivery sources"),
		KeySettingsAboutWelcome:          textEntry("Welcome"),
		KeyPickerPromptSearch:            textEntry("Search"),
		KeyPickerFooterOpenRows:          textEntry("open rows"),
		KeyPickerFooterBack:              textEntry("back"),
		KeyPickerFooterClose:             textEntry("close"),
		KeyPickerEmptyNoMatches:          textEntry("No matches"),
		KeyWelcomeShellTitle:             textEntry("Welcome to projmux"),
		KeyUpdateStatusAvailable:         textEntry("Update available"),
		KeyHelpUsageCommand:              textEntry("Usage"),
	},
	Locale("ko-KR"): {
		KeyNotifyAIResponseComplete:  textEntry("응답 완료"),
		KeyNotifyAIApprovalRequired:  textEntry("승인 필요"),
		KeyNotifyAIInputRequired:     textEntry("입력 필요"),
		KeyNotifyAIError:             textEntry("오류"),
		KeyNotifyAIReviewPending:     textEntry("검토 대기:"),
		KeySettingsRootNotifications: textEntry("알림"),
		KeySettingsRootProject:       textEntry("프로젝트"),
		KeySettingsRootAppearance:    textEntry("모양"),
		KeyPickerPromptSearch:        textEntry("검색"),
		KeyPickerFooterClose:         textEntry("닫기"),
		KeyPickerEmptyNoMatches:      textEntry("일치하는 항목 없음"),
		KeyWelcomeShellTitle:         textEntry("projmux 시작"),
	},
}

func textEntry(value string) Entry {
	return Entry{Kind: MessageKindText, Value: value}
}
