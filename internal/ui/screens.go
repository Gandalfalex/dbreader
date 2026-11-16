package ui

type screen int

const (
	scrConns screen = iota
	scrSchemas
	scrTables
	scrScripts // SQL files linked to the connection
	scrScript  // one file, as text
	scrGrep    // tables with rows matching a search
	scrData
	scrRow
	scrStruct
	scrHelp
	scrErrors
)

type inputMode int

const (
	inputNone inputMode = iota
	inputFilter
	inputSQL
	inputCommand
	inputConfirm
	inputFind   // / in a script
	inputSearch // ? : search rows on the server
)
