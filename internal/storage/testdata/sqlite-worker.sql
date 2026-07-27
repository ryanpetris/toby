
				PRAGMA busy_timeout=10000;
				BEGIN IMMEDIATE;
				WITH RECURSIVE sequence(value) AS (
					SELECT 1
					UNION ALL
					SELECT value + 1 FROM sequence WHERE value < %d
				)
				INSERT INTO records(worker, value)
				SELECT %d, value FROM sequence;
				COMMIT;
