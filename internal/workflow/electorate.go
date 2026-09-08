// ABOUTME: Parses poll electorate rows into explicit participant address groups.
// ABOUTME: It normalizes entitlement keys without inferring identity across rows.
package workflow

import (
	"fmt"
	"net/mail"
	"strings"
)

type Address struct {
	Delivery   string
	Normalized string
}

type Participant struct {
	ID        string
	Addresses []Address
}

const MaxParticipantDisplayNameRunes = 100

func ParseElectorate(rows []string) ([]Participant, error) {
	participants := make([]Participant, 0, len(rows))
	owners := make(map[string]int)
	for rowIndex, row := range rows {
		parts := strings.FieldsFunc(row, func(character rune) bool {
			return character == ',' || character == ':'
		})
		if strings.TrimSpace(row) == "" || len(parts) == 0 || hasEmptyAddress(row) {
			return nil, fmt.Errorf("participant row %d contains an empty address", rowIndex+1)
		}
		participant := Participant{}
		seen := make(map[string]struct{})
		for _, part := range parts {
			delivery := strings.TrimSpace(part)
			parsed, err := mail.ParseAddress(delivery)
			if err != nil || parsed.Name != "" || parsed.Address != delivery {
				return nil, fmt.Errorf("participant row %d contains an invalid bare address", rowIndex+1)
			}
			normalized := strings.ToLower(parsed.Address)
			if _, duplicate := seen[normalized]; duplicate {
				continue
			}
			if owner, duplicate := owners[normalized]; duplicate && owner != rowIndex {
				return nil, fmt.Errorf("address %q belongs to more than one participant", delivery)
			}
			seen[normalized] = struct{}{}
			owners[normalized] = rowIndex
			participant.Addresses = append(participant.Addresses, Address{Delivery: delivery, Normalized: normalized})
		}
		participants = append(participants, participant)
	}
	return participants, nil
}

func hasEmptyAddress(row string) bool {
	trimmed := strings.TrimSpace(row)
	return strings.HasPrefix(trimmed, ",") || strings.HasPrefix(trimmed, ":") ||
		strings.HasSuffix(trimmed, ",") || strings.HasSuffix(trimmed, ":") ||
		strings.Contains(trimmed, ",,") || strings.Contains(trimmed, "::") ||
		strings.Contains(trimmed, ",:") || strings.Contains(trimmed, ":,")
}
