package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/errcode"
	"github.com/99designs/gqlgen/graphql/executor"
	"github.com/stashapp/stash/internal/analytics"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Use the installed executor/validator in memory, with no resolvers, database,
// HTTP socket, or telemetry client. The same event constructor prepares capture.
func TestGraphQLVariableValidationContext(t *testing.T) {
	tests := []struct{ name, query, field string }{
		{"direct", `query JaneSmithPrivateOperation($JaneSmithPrivateVariable:FindFilterType) { JaneSmithPrivateAlias:findScenes(filter:$JaneSmithPrivateVariable) { count } }`, "findScenes"},
		{"fragment", `query JaneSmithPrivateOperation($JaneSmithPrivateVariable:FindFilterType) { ...JaneSmithPrivateFragment } fragment JaneSmithPrivateFragment on Query { JaneSmithPrivateAlias:findScenes(filter:$JaneSmithPrivateVariable) { count } }`, "findScenes"},
		{"inline fragment", `query JaneSmithPrivateOperation($JaneSmithPrivateVariable:FindFilterType) { ... on Query { findScenes(filter:$JaneSmithPrivateVariable) { count } } }`, "findScenes"},
		{"multiple consumers", `query JaneSmithPrivateOperation($JaneSmithPrivateVariable:FindFilterType) { findScenes(filter:$JaneSmithPrivateVariable) { count } findImages(filter:$JaneSmithPrivateVariable) { count } }`, "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exec := executor.New(NewExecutableSchema(Config{Resolvers: &Resolver{}}))
			op, errs := exec.CreateOperationContext(graphql.StartOperationTrace(context.Background()), &graphql.RawParams{
				Query: tc.query, OperationName: "JaneSmithPrivateOperation",
				Variables: map[string]any{"JaneSmithPrivateVariable": map[string]any{"JaneSmithPrivateKey": "JaneSmithPrivateMedia"}},
			})
			if len(errs) != 1 || errs[0].Message != "unknown field" {
				t.Fatalf("unexpected validation failure: %v", errs)
			}
			ctx := graphql.WithOperationContext(context.Background(), op)
			if got := graphQLValidationField(ctx, errs[0]); got != tc.field {
				t.Fatalf("field=%q want %q", got, tc.field)
			}
			_, operation := graphQLDiagnosticContext(ctx)
			code := errorCode(errs[0])
			if operation != "query" || code != errcode.ValidationFailed {
				t.Fatalf("operation=%q code=%q", operation, code)
			}
			diagnostic := graphQLRequestDiagnostic(ctx, errs[0])
			event := analytics.FailureException(diagnostic, "graphql", map[string]any{"graphql_field": tc.field, "graphql_operation_type": operation, "graphql_error_code": code}, nil)
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "JaneSmithPrivate") {
				t.Fatal("request data leaked into diagnostic event")
			}
			if !strings.Contains(string(encoded), "GraphQL validation: unknown field") {
				t.Fatal("validation cause lost")
			}
			// Identity extraction must not mutate the original client response path.
			if errs[0].Path[1] != ast.PathName("JaneSmithPrivateVariable") {
				t.Fatal("client error path mutated")
			}
			if tc.name == "direct" {
				op.RawQuery = strings.Repeat(" ", 256*1024+1)
				if graphQLValidationField(ctx, errs[0]) != "unknown" {
					t.Fatal("oversized query context retained")
				}
				op.RawQuery = tc.query
				op.Operation.SelectionSet = append(op.Operation.SelectionSet, make(ast.SelectionSet, 4096)...)
				if graphQLValidationField(ctx, errs[0]) != "unknown" {
					t.Fatal("partial field attribution escaped traversal budget")
				}
			}
		})
	}
}

func TestGraphQLFrameworkDiagnosticCodes(t *testing.T) {
	for _, code := range []string{errcode.ParseFailed, errcode.ValidationFailed} {
		err := &gqlerror.Error{Message: "diagnostic", Extensions: map[string]any{"code": code}}
		if got := errorCode(err); got != code {
			t.Fatalf("got %q want %q", got, code)
		}
	}
	if got := errorCode(&gqlerror.Error{Message: "diagnostic", Extensions: map[string]any{"code": "JaneSmithPrivateCode"}}); got != "UNKNOWN" {
		t.Fatal("unknown code escaped", got)
	}
}
