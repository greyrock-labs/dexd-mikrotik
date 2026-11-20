package mikrotik

// rosRecord is the RouterOS /ip/dns/static wire shape. RouterOS returns every
// field as a JSON string.
type rosRecord struct {
	ID       string `json:".id,omitempty"`
	Name     string `json:"name,omitempty"`
	Type     string `json:"type,omitempty"` // absent = A
	Address  string `json:"address,omitempty"`
	CNAME    string `json:"cname,omitempty"`
	Text     string `json:"text,omitempty"`
	TTL      string `json:"ttl,omitempty"`
	Comment  string `json:"comment,omitempty"`
	Regexp   string `json:"regexp,omitempty"`
	Disabled string `json:"disabled,omitempty"`
	Dynamic  string `json:"dynamic,omitempty"`
}

type rosResource struct {
	Version   string `json:"version"`
	BoardName string `json:"board-name"`
}
