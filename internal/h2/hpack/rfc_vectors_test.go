// Code generated bởi testdata/gen_vectors.py từ rfc7541.txt (curl https://www.rfc-editor.org/rfc/rfc7541.txt, 2026-10-01) — Appendix C.
package hpack

type rfcVector struct {
	id   string
	hex  string
	list []string // "name: value" đúng như RFC in
	size int      // Table size sau khi decode
}

var rfcVectors = []rfcVector{
	{id: "C.2.1", hex: "400a637573746f6d2d6b65790d637573746f6d2d686561646572", list: []string{"custom-key: custom-header"}, size: 55},
	{id: "C.2.2", hex: "040c2f73616d706c652f70617468", list: []string{":path: /sample/path"}, size: 0},
	{id: "C.2.3", hex: "100870617373776f726406736563726574", list: []string{"password: secret"}, size: 0},
	{id: "C.2.4", hex: "82", list: []string{":method: GET"}, size: 0},
	{id: "C.3.1", hex: "828684410f7777772e6578616d706c652e636f6d", list: []string{":method: GET", ":scheme: http", ":path: /", ":authority: www.example.com"}, size: 57},
	{id: "C.3.2", hex: "828684be58086e6f2d6361636865", list: []string{":method: GET", ":scheme: http", ":path: /", ":authority: www.example.com", "cache-control: no-cache"}, size: 110},
	{id: "C.3.3", hex: "828785bf400a637573746f6d2d6b65790c637573746f6d2d76616c7565", list: []string{":method: GET", ":scheme: https", ":path: /index.html", ":authority: www.example.com", "custom-key: custom-value"}, size: 164},
	{id: "C.4.1", hex: "828684418cf1e3c2e5f23a6ba0ab90f4ff", list: []string{":method: GET", ":scheme: http", ":path: /", ":authority: www.example.com"}, size: 57},
	{id: "C.4.2", hex: "828684be5886a8eb10649cbf", list: []string{":method: GET", ":scheme: http", ":path: /", ":authority: www.example.com", "cache-control: no-cache"}, size: 110},
	{id: "C.4.3", hex: "828785bf408825a849e95ba97d7f8925a849e95bb8e8b4bf", list: []string{":method: GET", ":scheme: https", ":path: /index.html", ":authority: www.example.com", "custom-key: custom-value"}, size: 164},
	{id: "C.5.1", hex: "4803333032580770726976617465611d4d6f6e2c203231204f637420323031332032303a31333a323120474d546e1768747470733a2f2f7777772e6578616d706c652e636f6d", list: []string{":status: 302", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:21 GMT", "location: https://www.example.com"}, size: 222},
	{id: "C.5.2", hex: "4803333037c1c0bf", list: []string{":status: 307", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:21 GMT", "location: https://www.example.com"}, size: 222},
	{id: "C.5.3", hex: "88c1611d4d6f6e2c203231204f637420323031332032303a31333a323220474d54c05a04677a69707738666f6f3d4153444a4b48514b425a584f5157454f50495541585157454f49553b206d61782d6167653d333630303b2076657273696f6e3d31", list: []string{":status: 200", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:22 GMT", "location: https://www.example.com", "content-encoding: gzip", "set-cookie: foo=ASDJKHQKBZXOQWEOPIUAXQWEOIU; max-age=3600; version=1"}, size: 215},
	{id: "C.6.1", hex: "488264025885aec3771a4b6196d07abe941054d444a8200595040b8166e082a62d1bff6e919d29ad171863c78f0b97c8e9ae82ae43d3", list: []string{":status: 302", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:21 GMT", "location: https://www.example.com"}, size: 222},
	{id: "C.6.2", hex: "4883640effc1c0bf", list: []string{":status: 307", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:21 GMT", "location: https://www.example.com"}, size: 222},
	{id: "C.6.3", hex: "88c16196d07abe941054d444a8200595040b8166e084a62d1bffc05a839bd9ab77ad94e7821dd7f2e6c7b335dfdfcd5b3960d5af27087f3672c1ab270fb5291f9587316065c003ed4ee5b1063d5007", list: []string{":status: 200", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:22 GMT", "location: https://www.example.com", "content-encoding: gzip", "set-cookie: foo=ASDJKHQKBZXOQWEOPIUAXQWEOIU; max-age=3600; version=1"}, size: 215},
}
