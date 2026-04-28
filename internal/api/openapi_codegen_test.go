package api

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"net/http"
	"sort"
	"strings"
	"testing"
	"unicode"
)

func TestOpenAPIReferencesResolve(t *testing.T) {
	doc := buildOpenAPIDocument()
	schemas := openAPISchemasFromDocument(t, doc)

	walkOpenAPIRefs(t, doc, "$", func(path, ref string) {
		const prefix = "#/components/schemas/"
		if !strings.HasPrefix(ref, prefix) {
			t.Fatalf("%s has unsupported ref %q", path, ref)
		}
		name := strings.TrimPrefix(ref, prefix)
		if _, ok := schemas[name]; !ok {
			t.Fatalf("%s references missing schema %q", path, name)
		}
	})
}

func TestOpenAPIGeneratedGoClientCompiles(t *testing.T) {
	doc := buildOpenAPIDocument()
	src := generateGoClientSmokeSource(t, doc)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "client.go", src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse generated client: %v\n%s", err, src)
	}

	conf := types.Config{Importer: importer.Default()}
	if _, err := conf.Check("openapiclient", fset, []*ast.File{file}, nil); err != nil {
		t.Fatalf("type-check generated client: %v\n%s", err, src)
	}
}

func walkOpenAPIRefs(t *testing.T, value any, path string, visit func(path, ref string)) {
	t.Helper()
	switch v := value.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			visit(path+".$ref", ref)
		}
		for key, child := range v {
			walkOpenAPIRefs(t, child, path+"."+key, visit)
		}
	case []any:
		for i, child := range v {
			walkOpenAPIRefs(t, child, fmt.Sprintf("%s[%d]", path, i), visit)
		}
	case []map[string]any:
		for i, child := range v {
			walkOpenAPIRefs(t, child, fmt.Sprintf("%s[%d]", path, i), visit)
		}
	}
}

func generateGoClientSmokeSource(t *testing.T, doc map[string]any) string {
	t.Helper()

	var src strings.Builder
	src.WriteString(`package openapiclient

import (
	"context"
	"net/http"
)

type Client struct {
	HTTPClient *http.Client
}

`)

	schemas := openAPISchemasFromDocument(t, doc)
	schemaNames := sortedMapKeys(schemas)
	for _, schemaName := range schemaNames {
		typeName, err := exportedGoIdentifier(schemaName)
		if err != nil {
			t.Fatalf("schema %q is not usable as a generated Go type: %v", schemaName, err)
		}
		src.WriteString(fmt.Sprintf("type %s map[string]any\n\n", typeName))
	}

	for _, op := range openAPIOperationsFromDocument(t, doc) {
		methodName, err := exportedGoIdentifier(op.operationID)
		if err != nil {
			t.Fatalf("operationId %q is not usable as a generated Go method: %v", op.operationID, err)
		}
		src.WriteString(fmt.Sprintf(`func (c *Client) %s(ctx context.Context) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, %q, %q, nil)
	if err != nil {
		return nil, err
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

`, methodName, strings.ToUpper(op.method), op.path))
	}

	return src.String()
}

type openAPIOperationDoc struct {
	method      string
	path        string
	operationID string
}

func openAPIOperationsFromDocument(t *testing.T, doc map[string]any) []openAPIOperationDoc {
	t.Helper()

	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("paths missing or wrong type")
	}

	var operations []openAPIOperationDoc
	for path, item := range paths {
		pathItem, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("path item %s has wrong type", path)
		}
		for method, rawOp := range pathItem {
			if _, ok := supportedOpenAPIMethods[strings.ToUpper(method)]; !ok {
				continue
			}
			op, ok := rawOp.(map[string]any)
			if !ok {
				t.Fatalf("operation %s %s has wrong type", method, path)
			}
			operationID, ok := op["operationId"].(string)
			if !ok || operationID == "" {
				t.Fatalf("operation %s %s has empty operationId", method, path)
			}
			operations = append(operations, openAPIOperationDoc{
				method:      method,
				path:        path,
				operationID: operationID,
			})
		}
	}

	sort.Slice(operations, func(i, j int) bool {
		if operations[i].operationID == operations[j].operationID {
			return operations[i].method+operations[i].path < operations[j].method+operations[j].path
		}
		return operations[i].operationID < operations[j].operationID
	})
	return operations
}

var supportedOpenAPIMethods = map[string]struct{}{
	http.MethodDelete: {},
	http.MethodGet:    {},
	http.MethodPatch:  {},
	http.MethodPost:   {},
	http.MethodPut:    {},
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func exportedGoIdentifier(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty identifier")
	}

	var out strings.Builder
	for i, r := range name {
		if i == 0 {
			if !isGoIdentifierStart(r) {
				return "", fmt.Errorf("first rune %q is not valid", r)
			}
			out.WriteRune(unicode.ToUpper(r))
			continue
		}
		if !isGoIdentifierPart(r) {
			return "", fmt.Errorf("rune %q is not valid", r)
		}
		out.WriteRune(r)
	}
	return out.String(), nil
}

func isGoIdentifierStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isGoIdentifierPart(r rune) bool {
	return isGoIdentifierStart(r) || unicode.IsDigit(r)
}
