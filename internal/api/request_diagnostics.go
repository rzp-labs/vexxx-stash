package api

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/errcode"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/lexer"
	"github.com/vektah/gqlparser/v2/parser"
)

// Share accepted capture identities across resolver and presenter boundaries,
// per operation (including individual operations on a WebSocket connection).
func graphQLCaptureState(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
	return next(diagnostics.WithState(ctx))
}

// Framework validation diagnostics can embed request names/literals even when
// no resolver (and therefore no FieldContext) exists. Change only the capture
// view, before the presenter replaces the framework code. Resolver messages
// retain the usual diagnostic treatment.
func graphQLRequestDiagnostic(ctx context.Context, err error) error {
	clean := graphQLDiagnosticError(err)
	gql, ok := err.(*gqlerror.Error)
	if !ok || gql.Err != nil || graphql.GetFieldContext(ctx) != nil {
		return clean
	}
	message := gql.Message
	if !graphql.HasOperationContext(ctx) {
		// Pinned POST decoder appends the entire body after this explicit delimiter.
		if strings.HasPrefix(message, "json request body could not be decoded:") {
			if reason, _, found := strings.Cut(message, " body:"); found {
				message = reason + " body:[request body redacted]"
			}
			return &graphQLDiagnosticMessage{message: message}
		}
		return clean
	}
	code, _ := gql.Extensions["code"].(string)
	if code != errcode.ParseFailed && code != errcode.ValidationFailed {
		return clean
	}
	op := graphql.GetOperationContext(ctx)
	phase := "GraphQL validation"
	if code == errcode.ParseFailed {
		phase = "GraphQL parse"
	}
	// Rule comes from pinned validator constructors, never a request parameter.
	if gql.Rule != "" {
		phase += " (" + gql.Rule + ")"
	}
	if op.Operation != nil && gql.Rule == "" {
		// VariableValues returns no coerced variables on failure. Its only string
		// value echo is the enum constructor; retain the enum type and reason.
		if i := strings.LastIndex(message, " is not a valid "); i >= 0 {
			message = "[request value redacted]" + message[i:]
		}
	}
	values, complete := graphQLRequestValues(op.RawQuery)
	if !complete || len(op.OperationName) > 256*1024 {
		return &graphQLDiagnosticMessage{message: phase + ": [request detail omitted: diagnostic input limit]"}
	}
	values = append(values, op.OperationName)
	// Invalid escapes can echo the unlexable remainder, so token fixtures alone
	// cannot cover them. These are exact lexer constructor prefixes, not a general
	// message allowlist. Keep the parse cause, omit the echoed escape/character.
	for _, prefix := range []string{"Invalid character escape sequence:", "Cannot parse the unexpected character"} {
		if strings.HasPrefix(message, prefix) {
			message = prefix + " [request token redacted]"
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	patterns := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		pattern := regexp.QuoteMeta(v)
		// Single-letter variables must not erase letters inside diagnostic words.
		if graphQLNameOrNumber.MatchString(v) {
			pattern = `\b` + pattern + `\b`
		}
		patterns = append(patterns, pattern)
	}
	if len(patterns) > 0 {
		redactor, err := regexp.Compile(strings.Join(patterns, "|"))
		if err != nil {
			return &graphQLDiagnosticMessage{message: phase + ": [request detail omitted: diagnostic input limit]"}
		}
		message = redactor.ReplaceAllString(message, "[request value redacted]")
	}
	return &graphQLDiagnosticMessage{message: phase + ": " + message}
}

var graphQLNameOrNumber = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

var graphQLSchemaNames = sync.OnceValue(func() map[string]bool {
	names := map[string]bool{"query": true, "mutation": true, "subscription": true, "fragment": true, "on": true, "true": true, "false": true, "null": true, "__typename": true}
	schema := NewExecutableSchema(Config{}).Schema()
	for name, def := range schema.Types {
		names[name] = true
		for _, f := range def.Fields {
			names[f.Name] = true
			for _, arg := range f.Arguments {
				names[arg.Name] = true
			}
		}
		for _, enum := range def.EnumValues {
			names[enum.Name] = true
		}
	}
	for name, def := range schema.Directives {
		names[name] = true
		for _, arg := range def.Arguments {
			names[arg.Name] = true
		}
	}
	return names
})

// Collect exact local redaction targets from bounded source tokens. Public
// schema vocabulary stays useful; caller-chosen operation/fragment/variable
// names and aliases stay private even if they happen to equal schema names.
func graphQLRequestValues(query string) ([]string, bool) {
	const tokenLimit = 4096
	if len(query) > 256*1024 {
		return nil, false
	}
	scan := lexer.New(&ast.Source{Input: query})
	values := []string{}
	names := graphQLSchemaNames()
	complete := false
	for i := 0; i < tokenLimit; i++ {
		token, err := scan.ReadToken()
		if err != nil || token.Kind == lexer.EOF {
			complete = true
			break
		}
		if token.Kind == lexer.String || token.Kind == lexer.BlockString || token.Kind == lexer.Int || token.Kind == lexer.Float || (token.Kind == lexer.Name && !names[token.Value]) {
			values = append(values, strconv.Quote(token.Value), token.Value)
		}
	}
	if !complete {
		return nil, false
	}
	// A syntactically valid document can be rejected before op.Doc is populated.
	// Reparse only the already bounded token stream; do not validate or execute it.
	doc, err := parser.ParseQueryWithTokenLimit(&ast.Source{Input: query}, tokenLimit)
	if err == nil {
		var walk func(ast.SelectionSet)
		walk = func(selections ast.SelectionSet) {
			for _, s := range selections {
				switch s := s.(type) {
				case *ast.Field:
					if s.Alias != s.Name {
						values = append(values, s.Alias)
					}
					walk(s.SelectionSet)
				case *ast.InlineFragment:
					walk(s.SelectionSet)
				case *ast.FragmentSpread:
					values = append(values, s.Name)
				}
			}
		}
		for _, op := range doc.Operations {
			values = append(values, op.Name)
			for _, v := range op.VariableDefinitions {
				values = append(values, v.Variable)
			}
			walk(op.SelectionSet)
		}
		for _, f := range doc.Fragments {
			values = append(values, f.Name)
			walk(f.SelectionSet)
		}
	}
	return values, true
}
