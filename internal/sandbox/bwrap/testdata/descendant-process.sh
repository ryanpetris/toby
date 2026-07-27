
shell=$1
sleep_command=$2
child_ready_path=$3
ready_path=$4
"$shell" -c 'trap "" TERM INT HUP QUIT; : > "$2"; exec "$1" 600' \
	toby-background-descendant "$sleep_command" "$child_ready_path" &
while [ ! -e "$child_ready_path" ]; do
	"$sleep_command" 0.01
done
: > "$ready_path"
wait
