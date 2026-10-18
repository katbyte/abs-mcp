package abs

// As is a client for the same server that sends another account's key, for a call only that account can make, such as writing its listening
// progress. It goes through this client's connection, so it is retried, logged and carried the same way, and keeps that key out of its errors
// as this one keeps its own. This file is written by hand.
func (c Client) As(apiKey string) (*Client, error) {
	other, err := New(c.Client.BaseURL, apiKey)
	if err != nil {
		return nil, err
	}
	other.Client.HTTPClient = c.Client.HTTPClient

	return other, nil
}
