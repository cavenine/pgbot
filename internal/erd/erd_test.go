package erd

import (
	"strings"
	"testing"
)

func fixture() Schema {
	return Schema{
		Tables: []Table{
			{Schema: "public", Name: "customers", Columns: []Column{
				{Name: "id", Type: "bigint", PK: true},
				{Name: "email", Type: "text"},
			}},
			{Schema: "public", Name: "order_items", Columns: []Column{
				{Name: "order_id", Type: "bigint", PK: true, FKTarget: "orders.id"},
				{Name: "product_id", Type: "bigint", PK: true, FKTarget: "products.id"},
			}},
			{Schema: "public", Name: "orders", Columns: []Column{
				{Name: "id", Type: "bigint", PK: true},
				{Name: "customer_id", Type: "bigint", FKTarget: "customers.id"},
			}},
			{Schema: "public", Name: "products", Columns: []Column{
				{Name: "id", Type: "bigint", PK: true},
			}},
		},
		Edges: []Edge{
			{FromTable: "order_items", FromColumn: "order_id", ToTable: "orders", ToColumn: "id"},
			{FromTable: "order_items", FromColumn: "product_id", ToTable: "products", ToColumn: "id"},
			{FromTable: "orders", FromColumn: "customer_id", ToTable: "customers", ToColumn: "id"},
		},
	}
}

// The terminal view: one box-drawn block per table with PK/FK markers, then a
// crow's-foot forest of relationships.
func TestRenderASCII(t *testing.T) {
	out := RenderASCII(fixture(), false)

	for _, want := range []string{
		"public.customers", "public.orders",
		"PK", "FK → customers.id",
		"┌", "└", "│", // box drawing
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ascii missing %q:\n%s", want, out)
		}
	}
	// The forest: parents own their children crow's-foot style, and a table
	// with two parents appears under one with a cross-link under the other.
	if !strings.Contains(out, "└─< orders (customer_id)") {
		t.Errorf("customers must own orders in the forest:\n%s", out)
	}
	if !strings.Contains(out, "─< order_items") {
		t.Errorf("order_items must appear as a child:\n%s", out)
	}
	// Deterministic: same input, same bytes.
	if out != RenderASCII(fixture(), false) {
		t.Error("render must be deterministic")
	}
}

// Mermaid output: valid erDiagram with relationships and typed columns —
// pasteable into GitHub or mermaid.live.
func TestRenderMermaid(t *testing.T) {
	out := RenderMermaid(fixture())
	for _, want := range []string{
		"erDiagram",
		"customers ||--o{ orders",
		`orders {`,
		"bigint id PK",
		"bigint customer_id FK",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mermaid missing %q:\n%s", want, out)
		}
	}
}

// Empty schemas render something honest, not a panic or blank screen.
func TestRenderEmpty(t *testing.T) {
	if out := RenderASCII(Schema{}, false); !strings.Contains(out, "no tables") {
		t.Errorf("empty must say so: %q", out)
	}
}
