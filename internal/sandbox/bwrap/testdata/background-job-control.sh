
		printf 'direct-background-ready:%s\n' "$$"
		kill -TSTP "$$"
		printf 'direct-background-child-resumed\n'
