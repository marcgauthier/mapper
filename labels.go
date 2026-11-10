package main

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// Label appearance lives in object metadata so layout, audit, undo, and live
// updates use the same lifecycle as other map items.
func validateLabelSpec(data map[string]any) error {
	fonts := map[string]bool{"sans-serif": true, "Arial": true, "Verdana": true, "Georgia": true, "Times New Roman": true, "Courier New": true, "monospace": true}
	for key, fallback := range map[string]string{"font_family": "sans-serif", "font_weight": "normal", "font_style": "normal", "font_color": "#172331"} {
		value, exists := data[key]
		if !exists {
			data[key] = fallback
			continue
		}
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be text", key)
		}
		text = strings.TrimSpace(text)
		valid := false
		switch key {
		case "font_family":
			valid = fonts[text]
		case "font_weight":
			valid = text == "normal" || text == "bold"
		case "font_style":
			valid = text == "normal" || text == "italic"
		case "font_color":
			if len(text) == 7 && text[0] == '#' {
				_, err := hex.DecodeString(text[1:])
				valid = err == nil
			}
		}
		if !valid {
			return fmt.Errorf("invalid %s", key)
		}
		data[key] = text
	}
	size := 24
	if value, exists := data["font_size"]; exists {
		n, err := strconv.Atoi(fmt.Sprint(value))
		if err != nil || n < 8 || n > 144 {
			return fmt.Errorf("font_size must be a whole number between 8 and 144")
		}
		size = n
	}
	data["font_size"] = size
	return nil
}
