
.timeout 10000
BEGIN IMMEDIATE;
.shell touch /toby/home/first-locked
.shell sh -c 'while [ ! -e /toby/home/second-started ]; do sleep 0.05; done'
.shell sleep 1
WITH RECURSIVE sequence(value) AS (
	SELECT 1
	UNION ALL
	SELECT value + 1 FROM sequence WHERE value < 100
)
INSERT INTO records(worker, value) SELECT 1, value FROM sequence;
COMMIT;
