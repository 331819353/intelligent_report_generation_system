package datasource

import (
	"regexp"
	"sort"
	"strings"
)

// DriverSpec is the single source of truth for externally connected database
// engines. Connector-specific behavior lives behind the driver type; callers
// use this catalog for labels, aliases, default ports and generic validation.
type DriverSpec struct {
	Type        Type
	DisplayName string
	Aliases     []string
	Schemes     []string
	DefaultPort int
	ExtraPorts  []int
}

var databaseDriverCatalog = []DriverSpec{
	{Type: TypeMySQL, DisplayName: "MySQL", Aliases: []string{"mysql"}, Schemes: []string{"mysql"}, DefaultPort: 3306},
	{Type: TypeMariaDB, DisplayName: "MariaDB", Aliases: []string{"mariadb", "maria db"}, Schemes: []string{"mariadb"}, DefaultPort: 3306},
	{Type: TypePostgreSQL, DisplayName: "PostgreSQL", Aliases: []string{"postgresql", "postgres", "pgsql"}, Schemes: []string{"postgresql", "postgres"}, DefaultPort: 5432},
	{Type: TypeOracle, DisplayName: "Oracle", Aliases: []string{"oracle"}, Schemes: []string{"oracle"}, DefaultPort: 1521},
	{Type: TypeSQLServer, DisplayName: "SQL Server", Aliases: []string{"sql server", "sqlserver", "mssql"}, Schemes: []string{"sqlserver", "mssql"}, DefaultPort: 1433},
	{Type: TypeClickHouse, DisplayName: "ClickHouse", Aliases: []string{"clickhouse", "click house"}, Schemes: []string{"clickhouse"}, DefaultPort: 8123},
}

var nonAliasCharacterPattern = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeDriverAlias(value string) string {
	return strings.Trim(nonAliasCharacterPattern.ReplaceAllString(strings.ToLower(value), " "), " ")
}

// DatabaseDrivers returns a defensive copy in stable UI order.
func DatabaseDrivers() []DriverSpec {
	result := make([]DriverSpec, len(databaseDriverCatalog))
	copy(result, databaseDriverCatalog)
	return result
}

func DatabaseDriver(value Type) (DriverSpec, bool) {
	for _, driver := range databaseDriverCatalog {
		if driver.Type == value {
			return driver, true
		}
	}
	return DriverSpec{}, false
}

func IsDatabaseType(value Type) bool {
	_, ok := DatabaseDriver(value)
	return ok
}

func IsSupportedType(value Type) bool {
	return value == TypeExcel || IsDatabaseType(value)
}

func DefaultDatabasePort(value Type) int {
	if driver, ok := DatabaseDriver(value); ok {
		return driver.DefaultPort
	}
	return 0
}

// DatabaseTypeFromText recognizes explicit engine names and URI schemes. It
// deliberately does not treat the generic word "sql" as SQL Server.
func DatabaseTypeFromText(value string) Type {
	normalized := " " + normalizeDriverAlias(value) + " "
	for _, driver := range databaseDriverCatalog {
		for _, alias := range append(append([]string{}, driver.Aliases...), driver.Schemes...) {
			candidate := normalizeDriverAlias(alias)
			if candidate != "" && strings.Contains(normalized, " "+candidate+" ") {
				return driver.Type
			}
		}
	}
	return ""
}

// DatabaseTypeFromPort returns a type only when the port is unambiguous. 3306
// intentionally resolves to MySQL because MariaDB is wire-compatible and must
// be named explicitly when its distinct identity matters.
func DatabaseTypeFromPort(port int) Type {
	candidates := make([]Type, 0, 2)
	for _, driver := range databaseDriverCatalog {
		ports := append([]int{driver.DefaultPort}, driver.ExtraPorts...)
		for _, candidate := range ports {
			if candidate == port {
				candidates = append(candidates, driver.Type)
				break
			}
		}
	}
	if port == 3306 {
		return TypeMySQL
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return ""
}

func DatabaseTypeValues() []string {
	values := make([]string, 0, len(databaseDriverCatalog))
	for _, driver := range databaseDriverCatalog {
		values = append(values, string(driver.Type))
	}
	sort.Strings(values)
	return values
}
