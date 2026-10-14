package main

import (
	"path"
	"slices"
	"strings"

	"github.com/tdewolff/parse/v2/js"
)

// models are the server's database records: Sequelize models, each a class with typed columns and links to other records.
type models struct {
	byClass map[*jsClass]*model
	byName  map[string]*model // by class name (LibraryItem) and by model name (libraryItem)
}

// model is one record: its class, its columns and the records it links to.
type model struct {
	class   *jsClass
	name    string // the model name, as Sequelize knows it
	columns []prop
	assocs  map[string]assoc
}

// assoc is a link to another record, one or many, under the name Sequelize gives it.
type assoc struct {
	target *model
	many   bool
}

// columnTypes are what Sequelize's DataTypes come back as in JSON. JSON columns are left unknown: the database does not say their shape.
var columnTypes = map[string]*jsType{
	"STRING": str, "TEXT": str, "CHAR": str, "UUID": str, "UUIDV4": str, "CITEXT": str, "ENUM": str, "DATEONLY": str,
	"INTEGER": integer, "BIGINT": integer, "SMALLINT": integer, "TINYINT": integer, "MEDIUMINT": integer,
	"FLOAT": num, "DOUBLE": num, "REAL": num, "DECIMAL": num,
	"BOOLEAN": boolean,
	"DATE":    date, "NOW": date,
}

// readModels finds every class under server/models that extends Sequelize's Model, then the links between them.
func readModels(p *program) *models {
	ms := &models{byClass: map[*jsClass]*model{}, byName: map[string]*model{}}

	for _, name := range sortedKeys(p.files) {
		if path.Dir(name) != "server/models" {
			continue
		}

		f := p.files[name]
		for _, cn := range sortedKeys(f.classes) {
			c := f.classes[cn]
			if identName(c.extends) != "Model" {
				continue
			}

			m := &model{class: c, name: lowerFirst(c.name), assocs: map[string]assoc{}}
			m.readInit()
			ms.byClass[c] = m
			ms.byName[c.name] = m
			ms.byName[m.name] = m
		}
	}

	for _, name := range sortedKeys(p.files) {
		if path.Dir(name) == "server/models" {
			ms.readAssocs(p.files[name])
		}
	}

	return ms
}

// readInit takes the columns and the model name from the class's static init, where it calls super.init({ columns }, { modelName }).
func (m *model) readInit() {
	init := m.class.statics["init"]
	if init == nil {
		return
	}

	timestamps := true
	walkStmts(init.Body.List, func(st js.IStmt) {
		for _, x := range stmtExprs(st) {
			walkExpr(x, func(x js.IExpr) {
				call, ok := x.(*js.CallExpr)
				if !ok || !isMember(call.X, "super", "init") || len(call.Args.List) < 1 {
					return
				}

				if cols, isObj := call.Args.List[0].Value.(*js.ObjectExpr); isObj {
					for _, p := range cols.List {
						if n := propertyName(p.Name); n != "" {
							m.columns = append(m.columns, prop{n, columnType(p.Value)})
						}
					}
				}

				if len(call.Args.List) < 2 {
					return
				}
				opts, ok := call.Args.List[1].Value.(*js.ObjectExpr)
				if !ok {
					return
				}
				for _, p := range opts.List {
					switch propertyName(p.Name) {
					case "modelName":
						if s, ok := stringValue(p.Value); ok {
							m.name = s
						}
					case "timestamps":
						if lit, ok := p.Value.(*js.LiteralExpr); ok && lit.TokenType == js.FalseToken {
							timestamps = false
						}
					}
				}
			})
		}
	})

	if timestamps {
		for _, n := range []string{"createdAt", "updatedAt"} {
			if m.column(n) == nil {
				m.columns = append(m.columns, prop{n, date})
			}
		}
	}
}

// columnType is a column's type from its declaration: DataTypes.STRING, DataTypes.STRING(255), or { type: DataTypes.STRING, allowNull: false }.
func columnType(v js.IExpr) *jsType {
	nullable := true
	if obj, ok := v.(*js.ObjectExpr); ok {
		v = nil
		for _, p := range obj.List {
			switch propertyName(p.Name) {
			case "type":
				v = p.Value
			case "allowNull":
				if lit, ok := p.Value.(*js.LiteralExpr); ok && lit.TokenType == js.FalseToken {
					nullable = false
				}
			case "primaryKey":
				nullable = false
			}
		}
	}

	if call, ok := v.(*js.CallExpr); ok {
		v = call.X
	}

	dot, ok := v.(*js.DotExpr)
	if !ok || identName(dot.X) != "DataTypes" {
		return unknown
	}

	t, ok := columnTypes[identName(dot.Y)]
	if !ok {
		return unknown
	}
	if nullable {
		return t.orNull()
	}

	return t
}

func (m *model) column(name string) *jsType {
	for _, c := range m.columns {
		if c.name == name {
			return c.typ
		}
	}

	return nil
}

// plain is a record as Sequelize writes it without a toJSON of its own: its columns, named apart from what toOldJSON writes.
func (m *model) plain() *jsType {
	return &jsType{kind: objectKind, name: m.class.name + "Record", props: slices.Clone(m.columns)}
}

// readAssocs takes every A.hasMany(B, { as }) and its kin in a file. A and B are a model's class, or a name taken from sequelize.models.
func (ms *models) readAssocs(f *jsFile) {
	visit := func(list []js.IStmt) {
		walkStmts(list, func(st js.IStmt) {
			for _, x := range stmtExprs(st) {
				walkExpr(x, func(x js.IExpr) {
					call, ok := x.(*js.CallExpr)
					if !ok || len(call.Args.List) < 1 {
						return
					}

					dot, ok := call.X.(*js.DotExpr)
					if !ok {
						return
					}

					kind := identName(dot.Y)
					if kind != "hasMany" && kind != "hasOne" && kind != "belongsTo" && kind != "belongsToMany" {
						return
					}

					from, to := ms.byName[identName(dot.X)], ms.byName[identName(call.Args.List[0].Value)]
					if from == nil || to == nil {
						return
					}

					many := kind == "hasMany" || kind == "belongsToMany"
					as, fk := "", ""
					if len(call.Args.List) > 1 {
						if opts, ok := call.Args.List[1].Value.(*js.ObjectExpr); ok {
							for _, p := range opts.List {
								switch propertyName(p.Name) {
								case "as":
									as, _ = stringValue(p.Value)
								case "foreignKey":
									fk = foreignKeyName(p.Value)
								}
							}
						}
					}

					if as == "" {
						as = to.name
						if many {
							as = plural(to.name)
						}
					}

					from.assocs[as] = assoc{target: to, many: many}

					// the column holding the link: on this record for belongsTo, on the other for hasMany and hasOne
					switch kind {
					case "belongsTo":
						if fk == "" {
							fk = as + "Id"
						}
						from.addKey(fk)
					case "hasMany", "hasOne":
						if fk == "" {
							fk = from.name + "Id"
						}
						to.addKey(fk)
					}
				})
			}
		})
	}

	for _, c := range f.classes {
		for _, m := range c.statics {
			visit(m.Body.List)
		}
	}
	visit(f.ast.List)
}

// addKey adds a link's column, an id held as text, unless the record declares it.
func (m *model) addKey(name string) {
	if m.column(name) == nil {
		m.columns = append(m.columns, prop{name, str.orNull()})
	}
}

// foreignKeyName is a link's column as its options name it: foreignKey: 'mediaId', or foreignKey: { name: 'mediaId' }.
func foreignKeyName(v js.IExpr) string {
	if s, ok := stringValue(v); ok {
		return s
	}

	if obj, ok := v.(*js.ObjectExpr); ok {
		for _, p := range obj.List {
			if propertyName(p.Name) == "name" {
				s, _ := stringValue(p.Value)
				return s
			}
		}
	}

	return ""
}

// plural is a model name as Sequelize makes it plural for a link to many: libraries, series, mediaProgresses.
func plural(s string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(s), "series"):
		return s
	case strings.HasSuffix(s, "ss"):
		return s + "es"
	case strings.HasSuffix(s, "y") && len(s) > 1 && !strings.ContainsRune("aeiou", rune(s[len(s)-2])):
		return s[:len(s)-1] + "ies"
	}

	return s + "s"
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}

	return strings.ToLower(s[:1]) + s[1:]
}
