package apps

import "net/url"

func authPluginScheme(input string) string {
	parsedURL, err := url.Parse(input)
	if err != nil {
		return ""
	}

	return parsedURL.Scheme
}
