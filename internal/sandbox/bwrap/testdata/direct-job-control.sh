
		for pass in 1 2; do
			printf 'direct-ready:%s:%s\n' "$$" "$pass"
			kill -TSTP "$$"
			printf 'direct-resumed:%s\n' "$pass"
		done
