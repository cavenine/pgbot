// Package erd renders a database's entity-relationship structure — tables,
// columns, keys, foreign-key edges — as a box-drawn terminal diagram or a
// Mermaid erDiagram. Structure only, never data: the same boundary as the
// schema_of MCP tool.
package erd

import (
	"fmt"
	"sort"
	"strings"
)

type Column struct {
	Name     string
	Type     string
	PK       bool
	FKTarget string // "table.column" when this column references another table
}

type Table struct {
	Schema  string
	Name    string
	Columns []Column
}

type Edge struct {
	FromTable  string // the referencing (child) table
	FromColumn string
	ToTable    string // the referenced (parent) table
	ToColumn   string
}

type Schema struct {
	Tables []Table
	Edges  []Edge
}

// RenderASCII draws one box per table (name, columns, PK/FK markers) and a
// crow's-foot relationship forest. Deterministic: sorted tables, sorted edges.
func RenderASCII(s Schema, color bool) string {
	if len(s.Tables) == 0 {
		return "no tables found (empty schema, or the role cannot see them)\n"
	}
	var b strings.Builder

	tables := append([]Table(nil), s.Tables...)
	sort.Slice(tables, func(i, j int) bool {
		if tables[i].Schema != tables[j].Schema {
			return tables[i].Schema < tables[j].Schema
		}
		return tables[i].Name < tables[j].Name
	})

	for _, t := range tables {
		writeTableBox(&b, t)
	}
	b.WriteString("\n")
	writeForest(&b, s)
	return b.String()
}

// writeTableBox renders one table:
//
//	┌─ public.orders ───────────────────┐
//	│ id           bigint   PK          │
//	│ customer_id  bigint   FK → customers.id │
//	└───────────────────────────────────┘
func writeTableBox(b *strings.Builder, t Table) {
	nameW, typeW := 0, 0
	for _, c := range t.Columns {
		nameW = max(nameW, len(c.Name))
		typeW = max(typeW, len(c.Type))
	}
	var rows []string
	for _, c := range t.Columns {
		marker := ""
		switch {
		case c.PK && c.FKTarget != "":
			marker = "PK FK → " + c.FKTarget
		case c.PK:
			marker = "PK"
		case c.FKTarget != "":
			marker = "FK → " + c.FKTarget
		}
		rows = append(rows, strings.TrimRight(
			fmt.Sprintf("%-*s  %-*s  %s", nameW, c.Name, typeW, c.Type, marker), " "))
	}
	title := t.Schema + "." + t.Name
	inner := len(title) + 4
	for _, r := range rows {
		inner = max(inner, len(r)+2)
	}
	fmt.Fprintf(b, "┌─ %s %s┐\n", title, strings.Repeat("─", inner-len(title)-3))
	for _, r := range rows {
		fmt.Fprintf(b, "│ %-*s│\n", inner-1, r)
	}
	fmt.Fprintf(b, "└%s┘\n", strings.Repeat("─", inner))
}

// writeForest prints the FK graph as parent-owns-children trees:
//
//	customers
//	 └─< orders (customer_id)
//	     └─< order_items (order_id)   · also < products
//
// Each child appears once, under its first (alphabetical) parent; additional
// parents show as a cross-link. Cycle-safe via a visited set.
func writeForest(b *strings.Builder, s Schema) {
	if len(s.Edges) == 0 {
		return
	}
	b.WriteString("Relationships\n")

	children := map[string][]Edge{} // parent → edges into it
	firstParent := map[string]string{}
	hasParent := map[string]bool{}
	edges := append([]Edge(nil), s.Edges...)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].ToTable != edges[j].ToTable {
			return edges[i].ToTable < edges[j].ToTable
		}
		return edges[i].FromTable < edges[j].FromTable
	})
	for _, e := range edges {
		children[e.ToTable] = append(children[e.ToTable], e)
		hasParent[e.FromTable] = true
		if _, ok := firstParent[e.FromTable]; !ok {
			firstParent[e.FromTable] = e.ToTable
		}
	}

	var roots []string
	for parent := range children {
		if !hasParent[parent] {
			roots = append(roots, parent)
		}
	}
	sort.Strings(roots)

	visited := map[string]bool{}
	var walk func(table, indent string)
	walk = func(table, indent string) {
		if visited[table] {
			return
		}
		visited[table] = true
		kids := children[table]
		for i, e := range kids {
			branch := "├─<"
			childIndent := indent + "│   "
			if i == len(kids)-1 {
				branch = "└─<"
				childIndent = indent + "    "
			}
			line := fmt.Sprintf("%s%s %s (%s)", indent, branch, e.FromTable, e.FromColumn)
			if firstParent[e.FromTable] != table {
				line += "  · also above"
				fmt.Fprintln(b, line)
				continue
			}
			fmt.Fprintln(b, line)
			walk(e.FromTable, childIndent)
		}
	}
	for _, r := range roots {
		fmt.Fprintln(b, r)
		walk(r, " ")
	}
	// Cycles (every member has a parent) still deserve printing.
	var leftovers []string
	for parent := range children {
		if !visited[parent] {
			leftovers = append(leftovers, parent)
		}
	}
	sort.Strings(leftovers)
	for _, r := range leftovers {
		fmt.Fprintln(b, r+"  (cycle)")
		walk(r, " ")
	}
}

// RenderMermaid emits a mermaid erDiagram — pasteable into GitHub markdown or
// mermaid.live for an interactive pan/zoom view.
func RenderMermaid(s Schema) string {
	var b strings.Builder
	b.WriteString("erDiagram\n")
	edges := append([]Edge(nil), s.Edges...)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].ToTable != edges[j].ToTable {
			return edges[i].ToTable < edges[j].ToTable
		}
		return edges[i].FromTable < edges[j].FromTable
	})
	for _, e := range edges {
		fmt.Fprintf(&b, "    %s ||--o{ %s : %s\n", e.ToTable, e.FromTable, e.FromColumn)
	}
	tables := append([]Table(nil), s.Tables...)
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	for _, t := range tables {
		fmt.Fprintf(&b, "    %s {\n", t.Name)
		for _, c := range t.Columns {
			marker := ""
			switch {
			case c.PK && c.FKTarget != "":
				marker = " PK, FK"
			case c.PK:
				marker = " PK"
			case c.FKTarget != "":
				marker = " FK"
			}
			// Mermaid types must be bare words: "character varying(64)" breaks it.
			typ := strings.NewReplacer(" ", "_", "(", "_", ")", "", ",", "_").Replace(c.Type)
			fmt.Fprintf(&b, "        %s %s%s\n", typ, c.Name, marker)
		}
		b.WriteString("    }\n")
	}
	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
