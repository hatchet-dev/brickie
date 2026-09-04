package overlay

func BindMount(source, target string) error {
	return bindMount(source, target)
}

func UnmountBind(target string) error {
	return unmountBind(target)
}
