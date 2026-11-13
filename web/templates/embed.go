package templates

import "embed"

//Embedd cannot deal with any kind of whatsoever path
//notation. It can only go down a path. And so on and
//so on. Therefore, this is the correct preferred
//Go solution.

//go:embed *.html
var ApplicationStartFileSystem embed.FS
