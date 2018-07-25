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

	toMysqlType = map[string]string{
		"BOOL":      "TINYINT(1)",
		"INT64":     "BIGINT",
		"FLOAT64":   "DOUBLE",
		"STRING":    "VARCHAR",
		"BYTES":     "BLOB",
		"DATE":      "DATE",
		"TIMESTAMP": "TIMESTAMP",
	}
)

func getMysqlType(t string) (string, error) {
	origType := t

	index := strings.Index(t, "(")
	if index != -1 {
		// Either STRING or BYTES
		origType = t[:index]
	}

	convertedType := ""
	if v, ok := toMysqlType[origType]; ok {
		convertedType = v

		// Replace too big VARCHAR to TEXT or append length attribute for VARCHAR
		// TODO more strict check
		if convertedType == "VARCHAR" {
			if t[index:] == "(MAX)" {
				convertedType = "TEXT"
			} else {
				convertedType += t[index:]
			}
		}
	} else {
		return "", invalidSpannerErr
	}

	return convertedType, nil
}

func getColumns(ct types.CreateTableStatement) ([]string, error) {
	var cols []string

	for _, col := range ct.Columns {
		convertedType, err := getMysqlType(col.Type)
		if err != nil {
			return []string{}, err
		}

		defaultValue := ""
		// TIMESTAMP doesn't allow implicit default value
		if convertedType == "TIMESTAMP" && col.Nullability == "NOT NULL" {
			defaultValue = "DEFAULT CURRENT_TIMESTAMP"
		}
		cols = append(cols, fmt.Sprintf("  `%s` %s %s %s", col.Name, convertedType, col.Nullability, defaultValue))
	}

	return cols, nil
}

func getPrimaryKey(ct types.CreateTableStatement) (string, error) {
	expectedLen := len(ct.PrimaryKeys)
	keyNames := make([]string, 0, expectedLen)

	for _, pk := range ct.PrimaryKeys {
		for _, col := range ct.Columns {
			if col.Name == pk.Name {
				// Check precondition
				if col.Nullability != "NOT NULL" {
					return "", invalidKeyErr
				}

				kn := fmt.Sprintf("`%s`", pk.Name)
				mysqlType, err := getMysqlType(col.Type)

				if err == nil && mysqlType == "TEXT" || mysqlType == "BLOB" {
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

func getRelation(child types.CreateTableStatement, maybeParents []types.CreateTableStatement) (string, error) {
	// no relation
	if child.Cluster.TableName == "" {
		return "", nil
	}

	var parent *types.CreateTableStatement
	for _, s := range maybeParents {
		if child.Cluster.TableName == s.Id {
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

	return fmt.Sprintf("  FOREIGN KEY (`%s`) REFERENCES `%s` (`%s`)", keyCol.Name, parent.Id, keyCol.Name), nil
}

func GetMysqlCreateTables(statements types.DDStatements) (string, error) {
	converted := ""

	for _, ct := range statements.CreateTables {
		converted += fmt.Sprintf("CREATE TABLE %s (\n", ct.Statement.Id)

		defs, err := getColumns(ct)
		if err != nil {
			return "", err
		}

		pk, err := getPrimaryKey(ct)
		if err != nil {
			// Skip key error
			if err != invalidKeyErr {
				return "", err
			}
		} else {
			defs = append(defs, pk)
		}

		// Convert interleave to foreign key
		relation, err := getRelation(ct, statements.CreateTables)
		if err != nil {
			return "", err
		}
		if relation != "" {
			defs = append(defs, relation)
		}

		converted += strings.Join(defs, ",\n") + "\n);\n"
	}

	return header + converted, nil
}
