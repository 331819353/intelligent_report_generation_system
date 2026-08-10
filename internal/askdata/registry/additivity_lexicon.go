package registry

// AdditivityLexicon contains business words used only to produce
// non-authoritative suggestions. Callers may replace either list per domain;
// compilation and release validation never consume this lexicon.
type AdditivityLexicon struct {
	RatioTerms    []string
	SnapshotTerms []string
}

func DefaultAdditivityLexicon() AdditivityLexicon {
	return AdditivityLexicon{
		RatioTerms: []string{
			"率", "比", "占比", "均价", "客单", "完成率", "渗透", "周转",
		},
		SnapshotTerms: []string{
			"库存", "余额", "在册", "在职", "期末", "结存", "持仓",
		},
	}
}
