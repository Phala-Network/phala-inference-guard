package names

func QoSPath(path string) bool {
	if path == "" || path[0] != '/' {
		return false
	}
	for _, ch := range path {
		if ch >= 'a' && ch <= 'z' {
			continue
		}
		if ch >= 'A' && ch <= 'Z' {
			continue
		}
		if ch >= '0' && ch <= '9' {
			continue
		}
		switch ch {
		case '/', '_', '.', '-':
			continue
		default:
			return false
		}
	}
	return true
}

func OutputTokenField(field string) bool {
	if field == "" {
		return false
	}
	for index, ch := range field {
		if ch >= 'a' && ch <= 'z' {
			continue
		}
		if ch >= 'A' && ch <= 'Z' {
			continue
		}
		if ch == '_' {
			continue
		}
		if index > 0 && ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return true
}
