# Recovery artifacts

`iteration-5-restored-source-fragments.tgz` contains every Iteration 5 source file that survived the final sandbox restart, including the application service, HTTP API, agent-facing facade, PostgreSQL store, OpenBao adapter, process entry point and selected tests.

The complete Iteration 5 implementation had previously been restored and tested, but its unarchived working tree was lost when the sandbox restarted. The fragments are kept outside the Go module so the final cumulative project remains buildable and the recovered material is not silently discarded.
