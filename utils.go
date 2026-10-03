package main

import (
	"fmt"
	"guestbook/constants"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/css"
)

// Decode CSS identifiers and URL strings for policy checks, never for delivery.
func decodeCSSEscapes(value string) string {
	var decoded strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			decoded.WriteByte(value[i])
			continue
		}
		i++
		if i == len(value) {
			break
		}
		start := i
		for i < len(value) && i-start < 6 && strings.ContainsRune("0123456789abcdefABCDEF", rune(value[i])) {
			i++
		}
		if i > start {
			code, _ := strconv.ParseUint(value[start:i], 16, 32)
			if code == 0 || code > utf8.MaxRune || code >= 0xd800 && code <= 0xdfff {
				code = utf8.RuneError
			}
			decoded.WriteRune(rune(code))
			if i < len(value) && strings.ContainsRune(" \t\r\n\f", rune(value[i])) {
				if value[i] == '\r' && i+1 < len(value) && value[i+1] == '\n' {
					i++
				}
			} else {
				i--
			}
		} else if value[i] == '\r' || value[i] == '\n' || value[i] == '\f' {
			if value[i] == '\r' && i+1 < len(value) && value[i+1] == '\n' {
				i++
			}
		} else {
			decoded.WriteByte(value[i])
		}
	}
	return decoded.String()
}

func cssTokenPolicy(tokens []css.Token, fontSource bool) string {
	for _, token := range tokens {
		name := strings.ToLower(decodeCSSEscapes(string(token.Data)))
		switch token.TokenType {
		case css.URLToken:
			if !fontSource {
				return "URLs are allowed only in @font-face src declarations."
			}
			open := strings.IndexByte(name, '(')
			if open < 0 || !strings.HasSuffix(name, ")") {
				return "Malformed font URL."
			}
			// Preserve URL case while validating its decoded scheme and authority.
			value := decodeCSSEscapes(string(token.Data))
			value = strings.TrimSpace(value[open+1 : len(value)-1])
			if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') {
				value = value[1 : len(value)-1]
			}
			u, err := url.Parse(value)
			if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil || strings.ContainsAny(value, "\x00\r\n\t") {
				return "Font URLs must be absolute HTTPS URLs without credentials."
			}
		case css.FunctionToken:
			switch strings.TrimSuffix(name, "(") {
			case "url", "expression", "image", "image-set", "-webkit-image-set", "src":
				return "Custom CSS contains a disallowed URL or executable function."
			}
		case css.AtKeywordToken:
			switch name {
			case "@import", "@namespace", "@document", "@-moz-document":
				return "Custom CSS contains a disallowed external at-rule."
			}
		case css.CustomPropertyValueToken:
			lexer := css.NewLexer(parse.NewInputString(string(token.Data)))
			for {
				kind, data := lexer.Next()
				if kind == css.ErrorToken {
					if lexer.Err() != io.EOF {
						return "Malformed custom property."
					}
					break
				}
				if message := cssTokenPolicy([]css.Token{{TokenType: kind, Data: data}}, false); message != "" {
					return message
				}
			}
		case css.BadStringToken, css.BadURLToken:
			return "Custom CSS contains a malformed string or URL."
		default:
		}
	}
	return ""
}

func cssDeclarationPolicy(tokens []css.Token) string {
	var significant []css.Token
	for _, token := range tokens {
		if token.TokenType != css.WhitespaceToken && token.TokenType != css.CommentToken {
			significant = append(significant, token)
			if len(significant) == 2 {
				break
			}
		}
	}
	if len(significant) == 2 && significant[0].TokenType == css.IdentToken && significant[1].TokenType == css.ColonToken {
		switch strings.ToLower(decodeCSSEscapes(string(significant[0].Data))) {
		case "behavior", "-moz-binding", "moz-binding":
			return "Custom CSS contains a disallowed executable property."
		}
	}
	return ""
}

// The parser expects rule lists inside grouping at-rules, even in nested CSS.
// Build a validation-only equivalent with explicit & rules around nested groups.
// Newer grouping rules use the same grammar as @media. The delivered CSS is unchanged.
func cssParserInput(tokens []css.Token, ends map[int]int) (string, string) {
	var output strings.Builder
	writeTokens := func(start, end int) {
		for _, token := range tokens[start:end] {
			output.Write(token.Data)
		}
	}
	var writeBlock func(int, int, bool) string
	writeBlock = func(start, end int, nested bool) string {
		statement, first := start, -1
		for i := start; i < end; i++ {
			kind := tokens[i].TokenType
			if first < 0 && kind != css.WhitespaceToken && kind != css.CommentToken {
				first = i
			}
			if kind == css.LeftParenthesisToken || kind == css.FunctionToken || kind == css.LeftBracketToken {
				i = ends[i]
			} else if kind == css.LeftBraceToken {
				close := ends[i]
				if first >= 0 && tokens[first].TokenType == css.CustomPropertyNameToken {
					i = close
					continue
				}
				name := ""
				if first >= 0 && tokens[first].TokenType == css.AtKeywordToken {
					name = string(tokens[first].Data)
				}
				group := name == "@media" || name == "@supports" || name == "@layer" ||
					name == "@container" || name == "@scope" || name == "@starting-style"
				if name == "@container" || name == "@scope" || name == "@starting-style" {
					writeTokens(statement, first)
					output.WriteString("@media")
					writeTokens(first+1, i)
				} else {
					if nested && name == "" && first >= 0 &&
						tokens[first].TokenType != css.IdentToken && tokens[first].TokenType != css.DelimToken {
						output.WriteString("& ")
					}
					writeTokens(statement, i)
				}
				output.WriteByte('{')
				if group && nested {
					output.WriteString("&{")
				}
				if reason := writeBlock(i+1, close, name == "" || group && nested); reason != "" {
					return reason
				}
				if group && nested {
					output.WriteByte('}')
				}
				output.WriteByte('}')
				i, statement, first = close, close+1, -1
			} else if kind == css.SemicolonToken {
				if reason := cssDeclarationPolicy(tokens[statement:i]); reason != "" {
					return reason
				}
				writeTokens(statement, i+1)
				statement, first = i+1, -1
			}
		}
		if reason := cssDeclarationPolicy(tokens[statement:end]); reason != "" {
			return reason
		}
		writeTokens(statement, end)
		return ""
	}
	if reason := writeBlock(0, len(tokens), false); reason != "" {
		return "", reason
	}
	return output.String(), ""
}

func validateCSS(source string) (bool, string) {
	if len(source) > constants.MAX_CSS_LENGTH {
		return false, fmt.Sprintf("Custom CSS is too long. Maximum allowed length is %d bytes.", constants.MAX_CSS_LENGTH)
	}
	if !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return false, "Custom CSS must be valid UTF-8 without null characters."
	}
	// Raw-text terminators are forbidden even inside strings/comments and font faces.
	lower := strings.ToLower(source)
	for _, marker := range []string{"</style", "<style", "</script", "<script", "<svg", "</svg"} {
		if strings.Contains(lower, marker) {
			return false, "Custom CSS must not contain HTML tags or raw-text terminators."
		}
	}

	lexer := css.NewLexer(parse.NewInputString(source))
	type bracket struct {
		kind  css.TokenType
		index int
	}
	var brackets []bracket
	var tokens []css.Token
	ends := make(map[int]int)
	for {
		kind, data := lexer.Next()
		if kind == css.ErrorToken {
			if lexer.Err() != io.EOF {
				return false, "Custom CSS could not be tokenized."
			}
			break
		}
		if kind == css.BadStringToken || kind == css.BadURLToken ||
			kind == css.CommentToken && !strings.HasSuffix(string(data), "*/") ||
			kind == css.StringToken && (len(data) < 2 || data[len(data)-1] != data[0]) {
			return false, "Custom CSS contains an unterminated comment, string, or URL."
		}
		index := len(tokens)
		switch kind {
		case css.LeftBraceToken:
			brackets = append(brackets, bracket{css.RightBraceToken, index})
		case css.LeftBracketToken:
			brackets = append(brackets, bracket{css.RightBracketToken, index})
		case css.LeftParenthesisToken, css.FunctionToken:
			brackets = append(brackets, bracket{css.RightParenthesisToken, index})
		case css.RightBraceToken, css.RightBracketToken, css.RightParenthesisToken:
			if len(brackets) == 0 || brackets[len(brackets)-1].kind != kind {
				return false, "Custom CSS contains mismatched blocks."
			}
			ends[brackets[len(brackets)-1].index] = index
			brackets = brackets[:len(brackets)-1]
		default:
		}
		if kind == css.AtKeywordToken {
			data = []byte(strings.ToLower(decodeCSSEscapes(string(data))))
		} else if kind == css.FunctionToken && strings.EqualFold(decodeCSSEscapes(string(data)), "url(") {
			data = []byte("url(")
		}
		tokens = append(tokens, css.Token{TokenType: kind, Data: parse.Copy(data)})
	}
	if len(brackets) != 0 {
		return false, "Custom CSS contains an unclosed block."
	}

	normalized, reason := cssParserInput(tokens, ends)
	if reason != "" {
		return false, reason
	}
	parser := css.NewParser(parse.NewInputString(normalized), false)
	var blocks []string
	for {
		grammar, kind, data := parser.Next()
		if grammar == css.ErrorGrammar {
			if parser.Err() != io.EOF {
				return false, "Custom CSS contains invalid syntax."
			}
			break
		}
		name := strings.ToLower(decodeCSSEscapes(string(data)))
		inFontFace := len(blocks) > 0 && blocks[len(blocks)-1] == "@font-face"
		if inFontFace && grammar == css.DeclarationGrammar {
			switch name {
			case "font-family", "src", "font-style", "font-weight", "font-stretch", "font-display",
				"unicode-range", "font-feature-settings", "font-variation-settings", "font-named-instance",
				"size-adjust", "ascent-override", "descent-override", "line-gap-override", "font-language-override":
			default:
				return false, "@font-face contains an unsupported font descriptor."
			}
		} else if inFontFace && grammar != css.EndAtRuleGrammar && grammar != css.CommentGrammar {
			return false, "@font-face may contain only font declarations."
		}
		fontSource := grammar == css.DeclarationGrammar && name == "src" &&
			inFontFace
		if message := cssTokenPolicy([]css.Token{{TokenType: kind, Data: data}}, false); message != "" {
			return false, message
		}
		switch grammar {
		case css.AtRuleGrammar, css.BeginAtRuleGrammar, css.BeginRulesetGrammar, css.DeclarationGrammar, css.CustomPropertyGrammar, css.QualifiedRuleGrammar:
			if message := cssTokenPolicy(parser.Values(), fontSource); message != "" {
				return false, message
			}
		default:
		}
		switch grammar {
		case css.BeginAtRuleGrammar:
			blocks = append(blocks, name)
		case css.BeginRulesetGrammar:
			if len(blocks) > 0 && blocks[len(blocks)-1] == "@font-face" {
				return false, "@font-face may contain only font declarations."
			}
			blocks = append(blocks, "")
		case css.EndAtRuleGrammar, css.EndRulesetGrammar:
			if len(blocks) > 0 {
				blocks = blocks[:len(blocks)-1]
			}
		default:
		}
	}
	return true, ""
}

// CompareCSSWithThemes compares the submitted CSS with built-in themes and
// returns the name of the theme if it matches one of the built-in themes.
// Returns an empty string if the submitted CSS does not match any built-in
// theme (so it is a custom CSS).
func CompareCSSWithThemes(submittedCSS string) (string, error) {
	submittedCSS = strings.TrimSpace(submittedCSS)
	submittedCSS = strings.ReplaceAll(submittedCSS, "\r\n", "\n")

	// Check if top comment is from a built-in theme, if not exit early
	if !strings.HasPrefix(submittedCSS, "/* [::Built in::] ") {
		return "", nil
	}

	files, err := os.ReadDir(constants.BUILT_IN_THEMES_DIR)
	if err != nil {
		return "", err
	}

	for _, file := range files {
		if filepath.Ext(file.Name()) == ".css" {
			content, err := os.ReadFile(filepath.Join(constants.BUILT_IN_THEMES_DIR, file.Name()))
			if err != nil {
				return "", err
			}

			trimmedContent := strings.TrimSpace(string(content))
			if submittedCSS == trimmedContent {
				return file.Name(), nil
			}
		}
	}

	return "", nil
}
