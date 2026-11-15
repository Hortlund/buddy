package tasks

// SQLite is intentionally in-memory. Emit each row as it is read instead of
// accumulating a result set inside the bounded snippet process.
const chatSQLRunner = `import sys, sqlite3, json
source = open(sys.argv[1], encoding="utf-8").read() if len(sys.argv) > 1 else sys.stdin.read()
# Let the submitted SQL control its own transaction boundaries.
db = sqlite3.connect(":memory:", isolation_level=None)
def run(statement):
    cursor = db.execute(statement)
    if cursor.description:
        print(json.dumps({"columns": [c[0] for c in cursor.description]}))
        for row in cursor:
            print(json.dumps(list(row), default=lambda value: {"hex": value.hex()}))
    else:
        print(json.dumps({"rows_affected": max(0, cursor.rowcount)}))
statement = ""
for char in source:
    statement += char
    if char == ";" and sqlite3.complete_statement(statement):
        run(statement)
        statement = ""
if statement.strip():
    run(statement)
`
