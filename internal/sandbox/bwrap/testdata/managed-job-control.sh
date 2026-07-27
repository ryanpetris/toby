
		printf 'managed-ready:%s\n' "$$"
		IFS= read -r ignored
		size=$(stty size)
		printf 'managed-resumed:%s\n' "$size"
