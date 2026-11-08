package pages

import "github.com/Ahhhh-man/registry-ui/internal/registry"

// contentTypes are the ?type= filters, "" for all.
var contentTypes = []sortChoice{{"", "All"}, {"image", "Images"}, {"helm", "Helm charts"}, {"signature", "Signatures"}, {"artifact", "Other artifacts"}}

// typeChip is one filter in the "type-chips" template.
type typeChip struct {
	Value, Label, Href string
	Count              int // -1 when unknown
	Current            bool
}

// typeOf is the ?type= value that matches a manifest of kind k.
func typeOf(k registry.Kind) string {
	switch k {
	case registry.KindImage, registry.KindIndex:
		return "image"
	case registry.KindHelm:
		return "helm"
	case registry.KindSignature:
		return "signature"
	}
	return "artifact"
}

// typeChips makes the type filters, cur picked. counts holds the types
// known to be there, with how many match or -1 when counting them would
// cost calls; a type missing from it is hidden, unless it's cur.
func typeChips(cur string, counts map[string]int, href func(t string) string) []typeChip {
	var chips []typeChip
	for _, c := range contentTypes {
		n, ok := counts[c.Value]
		if !ok && c.Value != cur {
			continue
		}
		chips = append(chips, typeChip{Value: c.Value, Label: c.Label, Href: href(c.Value), Count: n, Current: c.Value == cur})
	}
	return chips
}

// noMatch is the empty list's title when the name filter q, the type
// filter t or both leave nothing; noun names the unfiltered list's items.
func noMatch(noun, t, q string) string {
	switch t {
	case "":
	case "helm":
		noun = "Helm charts"
	case "artifact":
		noun = "artifacts"
	default:
		noun = t + "s"
	}
	if q == "" {
		return "No " + noun + " here"
	}
	return "No " + noun + " match “" + q + "”"
}
