package spanner2mysql

import (
	"fmt"
	"strings"

	"github.com/syucream/spar/src/types"
)

const (
	// header text
	header = "-- Auto-generated by jackup. DO NOT EDIT!\n--\n\n"
	// MySQL requires fixed size index
	pseudoKeyLength = 255
)

var (
	invalidInterleaveErr = fmt.Errorf("Invalid interleave")
	invalidSpannerErr    = fmt.Errorf("Invalid spanner type")
	invalidKeyErr        = fmt.Errorf("Invalid key")

	toMysqlType = map[types.ScalarColumnTypeTag]string{
		types.Bool:      "TINYINT(1)",
		types.Int64:     "BIGINT",
		types.Float64:   "DOUBLE",
		types.String:    "VARCHAR",
		types.Bytes:     "BLOB",
		types.Date:      "DATE",
		types.Timestamp: "TIMESTAMP",
	}
)

type Spanner2MysqlConverter struct {
	Strict               bool
	AllowConvertString   bool
	AllowShotenIndexName bool
}

func (c *Spanner2MysqlConverter) getMysqlType(t types.ColumnType) (string, error) {
	convertedType := ""

	if v, ok := toMysqlType[t.TypeTag]; ok {
		convertedType = v
		// Replace too big VARCHAR to TEXT or append length attribute for VARCHAR
		if c.AllowConvertString && t.TypeTag == types.String {
			if t.Length > 256 {
				convertedType = "TEXT"
			} else {
				convertedType += fmt.Sprintf("%d", t.Length)
			}
		}
	} else {
		return "", invalidSpannerErr
	}

	return convertedType, nil
}

func (c *Spanner2MysqlConverter) getColumns(ct types.CreateTableStatement) ([]string, error) {
	var cols []string

	for _, col := range ct.Columns {
		convertedType, err := c.getMysqlType(col.Type)
		if err != nil {
			return []string{}, err
		}

		defaultValue := ""
		// TIMESTAMP doesn't allow implicit default value
		if convertedType == "TIMESTAMP" && col.NotNull {
			defaultValue = "DEFAULT CURRENT_TIMESTAMP"
		}

		nullability := "NULL"
		if col.NotNull {
			nullability = "NOT NULL"
		}

		cols = append(cols, fmt.Sprintf("  `%s` %s %s %s", col.Name, convertedType, nullability, defaultValue))
	}

	return cols, nil
}

func (c *Spanner2MysqlConverter) getPrimaryKey(ct types.CreateTableStatement) (string, error) {
	expectedLen := len(ct.PrimaryKeys)
	keyNames := make([]string, 0, expectedLen)

	for _, pk := range ct.PrimaryKeys {
		for _, col := range ct.Columns {
			if col.Name == pk.Name {
				// Check precondition
				if !col.NotNull {
					return "", invalidKeyErr
				}

				kn := fmt.Sprintf("`%s`", pk.Name)
				if mt, err := c.getMysqlType(col.Type); err == nil && (mt == "TEXT" || mt == "BLOB") {
					kn += fmt.Sprintf("(%d)", pseudoKeyLength)
				}

				keyNames = append(keyNames, kn)
			}
		}
	}

	if expectedLen == len(keyNames) {
		return fmt.Sprintf("  PRIMARY KEY (%s)", strings.Join(keyNames, ", ")), nil
	} else {
		return "", invalidKeyErr
	}
}

func (c *Spanner2MysqlConverter) getRelation(child types.CreateTableStatement, maybeParents []types.CreateTableStatement) (string, error) {
	// no relation
	if child.Cluster.TableName == "" {
		return "", nil
	}

	var parent *types.CreateTableStatement
	for _, s := range maybeParents {
		if child.Cluster.TableName == s.TableName {
			parent = &s
			break
		}
	}

	if parent == nil {
		return "", invalidInterleaveErr
	}

	var keyCol *types.Column
	for _, cc := range child.Columns {
		for _, pc := range parent.Columns {
			if cc.Name == pc.Name && cc.Type == pc.Type {
				keyCol = &cc
				break
			}
		}
	}

	if keyCol == nil {
		return "", invalidInterleaveErr
	}

	// FOREIGN KEY TO TEXT or BLOB isn't supported
	if mt, err := c.getMysqlType(keyCol.Type); err == nil || mt == "TEXT" || mt == "BLOB" {
		return "", invalidKeyErr
	}

	return fmt.Sprintf("  FOREIGN KEY (`%s`) REFERENCES `%s` (`%s`)", keyCol.Name, parent.TableName, keyCol.Name), nil
}

func (c *Spanner2MysqlConverter) getIndexes(table types.CreateTableStatement, indexes []types.CreateIndexStatement) []string {
	var strIndexes []string

	for _, i := range indexes {
		if table.TableName == i.TableName {
			keys := make([]string, 0, len(i.Keys))
			for _, k := range i.Keys {
				keys = append(keys, fmt.Sprintf("`%s`", k.Name))
			}

			if i.Unique {
				iname := i.IndexName
				if c.AllowShotenIndexName && len(iname) > 255 {
					iname = ""
				}
				strIndexes = append(strIndexes, fmt.Sprintf("  INDEX `%s` (%s)", iname, strings.Join(keys, ", ")))
			} else {
				strIndexes = append(strIndexes, fmt.Sprintf("  UNIQUE (%s)", strings.Join(keys, ", ")))
			}

		}
	}

	return strIndexes
}

func (c *Spanner2MysqlConverter) Convert(statements *types.DDStatements) (string, error) {
	converted := ""

	for _, ct := range statements.CreateTables {
		converted += fmt.Sprintf("CREATE TABLE %s (\n", ct.TableName)

		defs, err := c.getColumns(ct)
		if err != nil {
			return "", err
		}

		pk, err := c.getPrimaryKey(ct)
		if err != nil {
			if err != invalidKeyErr {
				return "", err
			}
		} else {
			defs = append(defs, pk)
		}

		// Convert interleave to foreign key
		relation, err := c.getRelation(ct, statements.CreateTables)
		if err != nil {
			if err != invalidKeyErr {
				return "", err
			}
		} else if relation != "" {
			defs = append(defs, relation)
		}

		// Convert CreateIndex'es to INDEX(...) or UNIQUE(...)
		defs = append(defs, c.getIndexes(ct, statements.CreateIndexes)...)

		converted += strings.Join(defs, ",\n") + "\n);\n"
	}

	return header + converted, nil
}
