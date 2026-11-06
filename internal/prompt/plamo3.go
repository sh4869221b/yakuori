package prompt

// Plamo3Identity identifies raw source text with language metadata and PFML3 glossary records.
func Plamo3Identity() Identity {
	return Identity{SchemaV1, "plamo3-translation",
		"<|plamo:bos|><|plamo:tag|>dataset<|plamo:msg|>translation",
		"<|plamo:tag|>input<|plamo:key|>lang<|plamo:val|>%s<|plamo:msg|>%s<|plamo:tag|>output<|plamo:key|>lang<|plamo:val|>%s<|plamo:msg|>",
		"plamo3-language-codes-v1", "<|plamo:tag|>glossary(<|plamo:index|>%s<|plamo:item|>%s)*"}
}
