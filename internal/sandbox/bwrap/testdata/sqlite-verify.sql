
PRAGMA journal_mode;
PRAGMA integrity_check;
SELECT count(*), count(DISTINCT worker), sum(value) FROM records;
