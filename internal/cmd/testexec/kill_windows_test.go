package main

// killedHow is how the end line says a binary ended by os.Process.Kill ended: on Windows,
// TerminateProcess(h, 1), as taskkill /F ends a process too.
const killedHow = "exited 0x00000001"
