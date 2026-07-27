
.shell touch /toby/home/second-started
.timeout 10000
BEGIN IMMEDIATE;
WITH RECURSIVE sequence(value) AS (
	SELECT 1
	UNION ALL
	SELECT value + 1 FROM sequence WHERE value < 100
)
INSERT INTO records(worker, value) SELECT 2, value FROM sequence;
COMMIT;
