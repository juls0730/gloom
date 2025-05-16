CREATE TABLE IF NOT EXISTS plugins (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    path TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL UNIQUE,
    /* domains is a comma-separated list of domains */
    domains TEXT NOT NULL
);
