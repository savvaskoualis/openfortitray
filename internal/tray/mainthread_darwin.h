#ifndef OFT_MAINTHREAD_DARWIN_H
#define OFT_MAINTHREAD_DARWIN_H

// oft_run_on_main_thread synchronously invokes the cgo-exported
// goMainThreadTrampoline(handle) on the process's real OS main thread via
// libdispatch, blocking the calling thread until it returns. If already
// called from the main thread, it invokes the trampoline directly --
// dispatch_sync onto the main queue FROM the main thread itself would
// deadlock (the queue can never drain while the thread that would drain it
// is the one blocked waiting).
void oft_run_on_main_thread(unsigned long handle);

#endif
