# Research Feasibility of a Go Runtime - Aug 2022

## Abstract

The lack of diversity and ease of use of Polkadot Runtimes is a barrier that stops Polkadot from living up to its full promise. The Polkadot community should as a whole address this problem. 

While there are several choices for implementing Polkadot Hosts, C++, Rust, and Go, the only option for writing Polkadot Runtimes is Rust. There are too many good things to say about Rust, but it is well-known that it has a steep learning curve. On the other hand, Go is a language focused on simplicity that is gaining popularity among software developers nowadays. It is modern, powerful, and fast, backed by Google and used in many of their software, thus making it an ideal candidate for implementing Polkadot Runtimes.
Arguably, other Blockchain networks (e.g. Cosmos) have gained significant adoption due to the lower barrier for entry (compared to Rust).

To be feasible to develop Polkadot Runtime in Go, there are technological challenges that need to be cleared out first. This research is aimed at those challenges.


## 1. Introduction

Writing Polkadot Runtimes in Go is exciting, mainly because of Go's simplicity and automatic memory management. However, there are doubts if Polkadot's design decisions are well suited to the design and toolchain support of Go. Notably, producing Wasm compatible with Polkadot is not supported in any Go toolchain and Polkadot design decisions around memory management could potentially make pointless some of Go's main selling points. Although the language specification doesn't mention how it should manage its memory, the Go community recognizes it as a language with GC.

So aren't we setting up ourselves for failure with Go right from the start?
This research aims to provide conclusions if Go is a suitable choice to write Polkadot Runtimes and further aid the development of a Go toolchain capable of producing compatible Wasm.

Here is a list of questions we are going to try to give answers to:
* What are the design decisions behind Polkadot's architecture - WebAssembly specification, Host/Runtime interaction, Runtime memory management?
* How well the incorporated WebAssembly specification in Polkadot aligns with the Go language capabilities - current limitations and upcoming proposals that might be leveraged?
* Which compiler framework is best suited for targeting Wasm from a language with GC-managed runtime - LLVM, Binaryen?
* How Go and TinyGo internals work and differ - compiler, runtime, GC?
* What are the challenges of implementing a Polkadot Runtime with GC-managed language like Go?

After all the background research is done, a proof of concept toolchain is provided together with the research that describes all the steps required to implement it and future improvements.

But why is it so important to write in Go beside the above said? The main reason is that the Polkadot ecosystem does not have enough diversity of Runtime implementations and developing a toolchain would allow the development of Go framework similar to Substrate that will address that issue.


## 2. Background

### 2.1. The design decisions behind Polkadot's architecture

In addition to the [Polkadot spec](https://github.com/w3f/polkadot-spec) here is a list of important points that deserve to be addressed.

#### 2.1.1. WebAssembly specification

The Runtime Wasm module targets [WebAssembly MVP](https://github.com/WebAssembly/design/blob/main/MVP.md) without any extensions enabled, which supports a limited set of instructions compared to WebAssembly 1.0.
Also, it is expected to have a very domain-specific API that consists of:
* imported Host provided functions (`ext_allocator_malloc_version_1`, `ext_allocator_free_version_1`, etc).
* imported Host provided memory.
* exported linker specific globals (`__heap_base`, `__data_end`).
* exported `__indirect_function_table` (WIP and not enabled currently).
* exported business logic API functions (`Core_version`, `Core_execute_block`, `Core_initialize_block`, etc).

```
Type: wasm
...
Imports:
  Functions:
    "env"."ext_allocator_malloc_version_1": [I32] -> [I32]
    "env"."ext_allocator_free_version_1": [I32] -> []
    ...
  Memories:
    "env"."memory": not shared (20 pages..)
  Tables:
  Globals:
Exports:
  Functions:
    "Core_version": [I32, I32] -> [I64]
    "Core_execute_block": [I32, I32] -> [I64]
    "Core_initialize_block": [I32, I32] -> [I64]
    ...
  Memories:
  Tables:
    "__indirect_function_table": FuncRef (352..352)
  Globals:
    "__data_end": I32 (constant)
    "__heap_base": I32 (constant)
```

```
 _________________________________________________________________________________________
| HOST                                                                                    |
|                                       _________________________________________         |
|                                      |               ALLOCATOR                 |        |
|                                      | ext_allocator_malloc,ext_allocator_free |        |
|                                      |_________________________________________|        |
|      ______________________________________________________|_______________________     |
|     | WASM                                                 | (imported)            |    |
|     |                                                      |                       |    |
|     |             _________________________________________▼_____________________  |    |
|     | (imported) |          |                  |           [xxxxx---]            | |    |
|  MEMORY  -----►  | Data     |         ◄- Stack | Heap -►                         | |    |
|     |            |__________|__________________|_________________________________| |    |
|     |            0     __data_end         __heap_base                  max memory  |    |
|     |                                                                              |    |
|     |                                                                              |    |
|     |______________________________________________________________________________|    |
|                                                                                         |
|_________________________________________________________________________________________|

```

#### 2.1.2. WASI requirements

Polkadot is a non-browser environment, but it is not an OS. It doesn't seek to provide a system-level API comparable to an OS like files, networking, or any other major part of the things provided by WASI.

#### 2.1.3. SCALE codec

Runtime data, coming in the form of byte code, needs to be as light as possible. The SCALE codec provides the capability of efficiently encoding and decoding it. Since it is built for little-endian systems, it is compatible with Wasm environments.

#### 2.1.4. Runtime calls

Each function call into the Runtime is done with newly allocated memory (via the shared allocator), either for sharing input data or results. Arguments are SCALE encoded into a byte array and copied into this section of the Wasm shared memory. Allocations do not persist between calls. It is important to note that the Runtime uses the same Host provided allocator for all heap allocations, so the Host is in charge of the Wasm heap memory management.
Data passing to the Runtime API is always SCALE encoded, Host API calls on the other hand try to avoid all encoding.

#### 2.1.5. Exported globals

It is expected from the Runtime to export `__heap_base` global indicating the beginning of the heap. It is used by the Host allocator to prevent memory allocations below that address and avoid clashes with the stack and data sections.

#### 2.1.6. Imported vs exported memory

Imported memory works a little bit better than exported memory since it avoids some edge cases, although it also has some downsides, however, it does not matter too much. Working with exported memory is almost certainly still supported. In fact, this is how it worked in the beginning. However, the current spec describes that memory should be made available to the Polkadot Runtime for import under the symbol name `memory`.

#### 2.1.7. External memory management

The design in which allocation functions are on the Host side is dictated by the fact that some of the Host functions might return buffers of data of unknown size. That means that the Wasm code cannot efficiently provide buffers upfront.

For example, let's examine the Host function that returns a given storage value. The storage value's size is not known upfront in the general case, so the Wasm caller cannot pre-allocate the buffer upfront. A potential solution is to first call the Host function without a buffer, which will return the value's size, and then do the second call passing a buffer of the required size. For some Host functions, caches could be put in place for mitigation, some other functions cannot be implemented in such model at all. To solve this problem, it was chosen to place the allocator on the Host side.
However, this is not the only possible solution, as there is an ongoing discussion about moving the allocator into the Wasm: [[1]](https://github.com/paritytech/substrate/issues/11883).
Notably, the allocator maintains some of its data structures inside the linear memory and some other structures outside.

#### 2.1.8. Concurrency requirments

The Runtime executes in serial, the parallelism is accomplished through a network of parallel running chains (Parachains). It is primarily because creating a semantic for deterministic parallel executing is really difficult in general.


## 2.2. Translating Go's language capabilities to WebAssembly MVP

It is important to see how well the language features of Go translate to WebAssembly, it's limitations and upcoming proposals that might help to overcome them. More specifically, the capabilities offered by WebAssembly MVP, targeted by Polkadot.
WebAssembly is low level bytecode instruction format for typed stack-based virtual machine, that uses little-endian byte order when translating between values and bytes. Incorporates harvard architecture - the program state is separate from the instructions, with implicit stack that can't be accessed, only the untrusted memory. It has single, contiguous, byte-addressable default linear memory, accessed by all memory operators, spanning from offset 0 and extending up to a varying memory size. It can either be imported or defined internally inside the module, but eiither way, after import or definition, there is no difference when accessing it.

### 2.2.1. Limitations

* the linear memory is great for languages with manual, reference counting or ownership memory model, but GC-managed memory requires a bit more work to port it. Since WebAssembly has no stack introspection to scan the roots on the stack, it requires to use mirrored shadow stack in the linear memory, pushed/poped along with the machine stack, which makes it less efficient.
* the linear memory can be resized, but only upward (`grow_memory`, `memory.grow`).

### 2.2.2. Proposals

* [GC proposal](https://github.com/WebAssembly/gc/blob/main/proposals/gc/Overview.md) might be handy to support an automatic memory management, but there are concerns how performant would be [[1]](https://github.com/WebAssembly/gc/issues/59), [[2]](https://github.com/WebAssembly/gc/issues/36). In addition to that, the Polkadot Runtime supports only WebAssembly MVP currently, also the GC proposal is under development and it is not yet clear if Polkadot will be able to leverage the GC proposal. Potential problems include determinism (is there anything in GC that causes ND? Can it be tamed efficiently?) and safety (Is it possible for a host to limit the resource consumption reliably and deterministically?).
* allow setting protection and creating mappings within the contiguous linear memory.
* there are only default linear memories, but new memory operators may be added after the MVP which can also access non-default memories.


## 2.3. Compiler backends targeting Wasm - LLVM, Binaryen

* LLVM is much more powerful as an optimizer.
* LLVM currently does not support Wasm GC, and GC is not the main focus for LLVM as most languages that utilize it use linear memory - C, C++, Rust, Zig.
* LLVM is larger in size compared to Binaryen.

* Binaryen compiles much more quickly.
* Binaryen does general-purpose optimizations to the wasm that LLVM does not, including whole-program optimizations.
* Binaryen is smaller in size compared to LLVM.
* Binaryen is a better choice for languages with GC, which intend to compile to Wasm GC. Additionally, it will support compilation from Wasm GC to Wasm MVP, as a polyfill, though the opposite will not be possible.
* In case Polkadot's wasm target switches to Wasm GC, having Binaryen as a compiler backend will have support for that, as the developers behind it are from WebAssembly organisation.


## 2.4. Go

### 2.4.1. Compiler

The default compiler is `gc`. There are also `gccgo` which uses the GCC back-end and `gollvm` which uses the LLVM infrastructure (somewhat less mature).

```
// Frontend Compiler (lexing, parsing, typechecking)

CODE -> tokenizer/lexer/scanner (lexical analysis) -> TOKENS STREAM
TOKENS STREAM -> go/parser (syntactic analysis) -> AST (annotated)
AST -> go/types (type check) -> AST (with types)
AST (with types) -> golang.org/x/tools/go/ssa (convert) -> SSA

// Optimization

* variable capturing, inlining, escape analysis, closure rewriting, walk

SSA (higher level with Go-specific constructs like interfaces and *goroutines*) -> convert (tinygo) -> LLVM IR
LLVM IR -> optimize (llvm) -> LLVM IR (optimize by a mixture of handpicked LLVM optimization passes, TinyGo-specific optimizations ()

// Backend Compiler

LLVM IR (optimized) -> convert (llvm) -> MACHINE CODE
MACHINE CODE -> convert (llvm) -> OBJECT FILE
OBJECT FILE -> link (llvm) -> EXECUTABLE
```

### 2.4.2. Runtime

Implements GC, scheduler included in every Go program. Contains a lot of type information at runtime.

#### 2.4.2.1. Memory Management

Process
* does not have direct access to the physical memory
* virtual memory abstracts the access to the physical memory (via segmentation and page tables)

Process Memory Layout
```
 ______________________
|        STACK         | Function Stack Frames
|----------------------| ◄--- Stack Pointer
|          |           |
|          ▼           |
|                      |
|          ▲           |
|          |           |
|----------------------|
|         HEAP         | Dynamic Allocated Variables
|----------------------|
|         BSS          | Uninitialized Static Variables
|----------------------|
|         DATA         | Initialized Static Variables
|----------------------|
|         TEXT         | Code
|______________________|
```

**Stack**
* managed by the compiler
* elastic
* one stack per *goroutine*

**Heap**
* allocated by the memory allocator and collected by the garbage collector
* it is not an entity and there is no linear containment of memory that defines the Heap
* any memory reserved for application use in the process space is available for heap memory allocation

Go uses escape analysis and Garbage Collector. Allocator is tightly coupled with the Garbage Collector.
```
   Mutator (Process)
      |
      | malloc/free
      |
      ▼
  Allocator
      |
      | syscall mmap/munmap (address, size, permissions, flags)
      |
 _____▼____
|          | 
|   Heap   | ◄------ Collector
|__________|
```

**Allocator**
* allocate new blocks with the correct size
* deals with fragmentation (merge smaller block to allow allocation of larger ones)

**Garbage Collector**
* tracking memory allocations in heap memory
* releasing allocations that are no longer needed
* keeping allocations that are still in use

* tracing garbage collector (not reference counting)
* hybrid stop-the-world/concurrent collector (stop-the-world part limited by a 10ms deadline)
* CPU cores dedicated to running the concurrent collector
* tri-color mark-and-sweep algorithm

* non-generational
* non-compacting
* fully precise
* incurs a small cost if the program is moving pointers around
* lower latency, but most likely also lower throughput

*collection*
1. Mark Setup (stop the world)
    * turn on write barrier
    * stop all goroutines

2. Marking (concurrent)
    * inspect the stack to find root pointers to the heap
    * traverse the heap graph from those root pointers
    * mark values on the heap that are still in use
    * slow down allocations to speed up collection

3. Mark Termination (stop the world)
    * turn the write barrier off
    * various cleanup tasks
    * next collection goal is calculated

*sweeping*
Freeing Heap Memory
* occurs when the *goroutines* attempt to allocate new heap memory
* the latency of sweeping is added to the cost of performing new allocation

Compiler decides (via escape analysis) when a value should be allocated on the Heap
* sharing down (passing pointers) typically stays on the Stack
* sharing up (returning pointers) typically escapes on the Heap

* when the value could be referenced after the function that constructed it returns
* if the value is too large to fit on the stack
* when the compiler doesn't know the size of the value at compile time

#### 2.4.2.2. Concurrency

The scheduler runs *goroutines*, pauses and resumes them on blocking channel ops. or mutex ops, coordinates blocking system calls, io, runtime GC. *Goroutines* use space threads, managed by the runtime.

#### 2.4.2.3. Parallelism


## 2.5. TinyGo

It is a subset of Go with very different goals from the standard Go. It is an alternative compiler and runtime aimed to support many different small embedded devices with a single processor core that require certain optimizations mostly toward size.

*Goals*
* Have very small binary sizes.
* Support for most common microcontroller boards.
* Be usable on the web using WebAssembly.
* Good CGo support, with no more overhead than a regular function call.
* Support most standard library packages and compile most Go code without modification.

*Non-goals*
* Using more than one core.
* Be efficient while using zillions of *goroutines*. However, good *goroutine* support is certainly a goal.
* Be as fast as `gc`. However, LLVM will probably be better at optimizing certain things so TinyGo might actually turn out to be faster for number crunching.
* Be able to compile every Go program out there.

### 2.5.1. Compiler

The compiler uses (mostly) the standard library to parse Go programs and LLVM to optimize the code and generate machine code for the target architecture.

**Pipeline (Lowering)**
```
// Frontend Compiler (lexing, parsing, typechecking)

CODE -> parse (go/parser) -> AST
AST -> type check (go/types) -> AST (with types)
AST (with types) -> convert (golang.org/x/tools/go/ssa) -> SSA

// Optimization

SSA (higher level with Go-specific constructs like interfaces and goroutines) -> convert (tinygo) -> LLVM IR
LLVM IR -> optimize (llvm) -> LLVM IR (optimize by a mixture of handpicked LLVM optimization passes, TinyGo-specific optimizations (escape analysis, string-to-[]byte optimizations, etc.) and custom lowering.)

// Backend Compiler

LLVM IR (optimized) -> convert (llvm) -> MACHINE CODE
MACHINE CODE -> convert (llvm) -> OBJECT FILE
OBJECT FILE -> link (llvm) -> EXECUTABLE
```

* root: contains the command line interface for the tinygo command and all its subcommands
* builder: orchestrates the build
* loader: loads and typechecks the code, and produces an AST
* compiler: the compiler itself, makes little attempt at optimizing code
* interp: tries to run package initializers at compile time as far as possible
* transform: implements various optimizations necessary to produce working and efficient code

### 2.5.2. Runtime

The runtime is written from scratch, optimized for size instead of speed and re-implements some compiler intrinsics and packages:

* Startup code (device specific runtime initialization of memory, timers)
* GC (copied from micropython, super simple, optimized for size, runs the GC once it runs out of memory)
* Memory Allocator
* *Goroutines* scheduler
* Channels
* Time handling (every chip has a clock)
* Hashmap implementation (optimized for size instead of speed)
* Runtime functions like maps, slice append, defer, ... (various things that are not implemented in the compiler)
* Operations on strings
* `sync` & `reflect` package (strongly connected to the runtime)

#### 2.5.2.1. Basic features

All basic types, slices, all regular control flow including switch, closures and bound methods are supported, `defer` keyword is almost entirely supported, with the exception of deferring some builtin functions, interfaces are quite stable and should work well in almost all cases. Type switches and type asserts are also supported, as well as calling methods on interfaces. The only exception is comparing two interface values.
Maps are usable but not complete. You can use any type as a value, but only some types are acceptable as map keys (strings, integers, pointers, and structs/arrays that contain only these types). Also, they have not been optimized for performance and will cause linear lookup times in some cases.

#### 2.5.2.2. Memory Management

Heap with a garbage collector. While not directly a language feature, garbage collection is important for most Go programs to make sure their memory usage stays in reasonable bounds.

Garbage collection is currently supported on all platforms, although it works best on 32-bit chips. A simple conservative mark-sweep collector is used that will trigger a collection cycle when the heap runs out (that is fixed at compile time) or when requested manually using `runtime.GC()`. Some other collector designs are used for other targets, TinyGo will automatically pick a good GC for a given target.

Careful design may avoid memory allocations in main loops. You may want to compile with `-print-allocs=.` to find out where allocations happen and why they happen.

#### 2.5.2.3. Concurrency

*Goroutines* and channels work for the most part, for platforms such as WebAssembly the support is a bit more limited (calling a blocking function may for example allocate heap memory).

#### 2.5.2.4. Parallelism

Single core only.


## 2.6. Technical challenges

TinyGo's design decisions are mostly based on optimizations around small embedded devices. Of course, this is good for the blockchain's use case too, but not always required and as crucial as is for devices with very limited resources. The necessity of rewriting a large part of Go's runtime to align with those optimizations contributes to the effort of supporting Go's capabilities. Another point where TinyGo diverges from the toolchain requirements of Polkadot is that it supports Wasm aiming at the most recent Webassembly features. Here is a detailed breakdown of most of the problematic points:

### 2.6.1. Toolchain support for Wasm for non-browser environments

* The official Go compiler does not support Wasm for non-browser environments [[1]](https://github.com/golang/go/issues/31105), only Wasm with browser-specific API.
* The alternative TinyGo compiler and runtime, supports Wasm outside the browser, but it is still not capable of producing Wasm compatible with Polkadot's requirements.

### 2.6.2. Wasm features that are not part of the MVP

TinyGo makes use of some features that are not supported in the targeted Wasm MVP, such as bulk memory operations (`memory.copy`, `memory.fill` used to reduce the code size) and other extensions.

### 2.6.3. Standard library support

* The standard library relies on the `reflect` package (most common types like numbers, strings, and structs are supported), which is not fully supported by TinyGo [[1]](https://github.com/tinygo-org/tinygo/pull/2640). The core primitives and SCALE serialization logic that we intended to reuse from [gossamer](https://github.com/ChainSafe/gossamer) also rely on the `reflect` package.
* Many features of Cgo are still unsupported (#cgo statements are only partially supported).

### 2.6.4. External memory allocator and GC

* According to the Polkadot specification, the Wasm module does not include a memory allocator. It imports memory from the Host and relies on Host imported functions for all heap allocations. TinyGo implements simple GC and manages its memory by itself, contrary to specification. So it can't work out of the box on systems where the Host wants to manage its memory.

### 2.6.5. Linker globals

* The Runtime is expected to expose `__heap_base` global [[1]](https://github.com/tinygo-org/tinygo/issues/2045), but TinyGo doesn't support that out of the box.

### 2.6.6. Developer experience

* Implementing Wasm functionality makes you go pretty low-level and use some "unsafe" language constructs.


## 3. Results

Taking into consideration all the technical challenges, the timeframe, and the number of different technologies, we propose a solution based on a modified version of TinyGo/LLVM, which is going to serve as a proof of concept. The goal is to implement a solution that incorporates runtime with GC and external memory allocator targeting Wasm MVP.

### 3.1. Proof of concept (alternative compiler + runtime + GC with external allocator)

**Add Toolchain support for Wasm compatible with Polkadot**
1. [x] Fork and add [tinygo](https://github.com/LimeChain/tinygo) as a submodule.
2. [x] Add separate Dockerfile and build script for building TinyGo with prebuild LLVM and deps (for faster builds).
3. Add new target similar to Rust's `wasm32-unknown-unknown`, but aimed to support Polkadot's Wasm MVP.
  * [x] add new target `polkawasm.json` in `targets/`
  * [x] add new `polkawasm` implementation in `runtime_polkawasm.go`
  * [x] use the `polkawasm` directive to separate the new target from the existing Wasm/WASI functionality.
  * [x] use linker flag to declare memory as imported.
  * [x] use linker flags to export `__heap_base`, `__data_end` globals.
  * [x] use linker flags to export `__indirect_function_table`.
  * [x] change the stack placement not to start from the beginning of the linear memory.
  * [x] disable the the scheduler to remove the support of *goroutines* and channels (and JS/WASI exports).
  * [x] remove the unsupported features by Wasm MVP (bulk memory operations, lang. ext) and add implementation of `memmove`, `memset`, `memcpy`, use the opt flag as part of the target.
  * [x] increase the memory size to 20 pages
  * [x] use the conservative GC as a starting point (there is a chance for memory corruption)
  * [x] add GC implementation that can work with external memory allocator (remove memory allocation exports).
  * [x] override the allocation functions used in the GC with such provided by the host.
  * [x] remove the exported allocation functions
  * [ ] the `_start` export func should be called somewhere to init the heap (the host does not support that).
  * [ ] better abstractions, the extalloc GC depends on third party allocation API that might change in the future.

**Setup Polkadot Host**
1. [x] Fork and add [gossamer](https://github.com/LimeChain/gossamer) as a submodule.
2. [x] Make the necessary changes to run locally provided Runtime inside the Host.
3. [x] Setup test instance to run the compiled Wasm (target MVP, import host provided functions and memory, implement bump allocator).

**Implement Polkadot Runtime**
1. [x] Implement SCALE codec without reflection.
2. Implement the minimal Runtime API (core API).
  * [x] `Core_version`
  * [ ] `Core_execute_block`
  * [ ] `Core_initialize_block`
3. [x] Add Makefile steps

**Future toolchain improvements**
1. [ ] write performance tests.
2. [ ] complete the SCALE codec implementation.
3. [ ] complete the reflect packages support
4. [ ] extalloc GC might need more work.
5. [ ] fix output errors in the Wasm memory.

SCALE codec
* [ ] fix: `panic("Assertion error: n>4 needed to compact-encode uint64")`
* [ ] fix: `Could not write " + strconv.Itoa(len(bytes)) + " bytes to writer`

TinyGo + conservative GC
* [ ] `runtimePanic("out of memory")`
* [ ] `runtimePanic("nil pointer dereference")`
* [ ] `runtimePanic("slice out of range")`
* [ ] `runtimePanic("index out of range")`

TinyGo + extalloc GC
* [ ] `return "reflect: call of reflect.Type." + e.Method + " on invalid type"`
* [ ] `return "reflect: call of " + e.Method + " on zero Value"`

* Gossamer instance freezes with Wasm compiled with extalloc GC while running the Core_version test. Here is the fragment that seems to cause that:
```go
  instance.ctx.Version, err = instance.version()
  if err != nil {
    instance.close()
    return nil, fmt.Errorf("getting instance version: %w", err)
  }
```

Comparisons

  * Core_version (empty APIs) from Substrate

    `\x10node8substrate-node\n\x00\x00\x00\f\x01\x00\x00\x00\x00\x00\x00\x00\x02\x00\x00\x00\x01`

  * Core_version (empty APIs) from Gosemble + conservative GC

    `\x10node8substrate-node\n\x00\x00\x00\f\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x00\x00\x01\x00\x00\x00`

  * Core_version (empty APIs) from Gosemble + extalloc GC

    `\x10node8substrate-node\n\x00\x00\x00\f\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x00\x00\x01\x00\x00\x00`

### 3.2. Testing Guide

The steps below will showcase testing a PoC Polkadot Runtime implementation in Go.

**Prerequisites**
- [git](https://git-scm.com/downloads)
- [Go 1.18+](https://golang.org/doc/install)
- [docker](https://docs.docker.com/install/)

**Cloning the repository**
```bash
git clone https://github.com/LimeChain/gosemble.git
cd gosemble
```

**Pull all necessary git submodules**
```bash
git submodule update --init --recursive
```

**Build the Runtime**

Using a [forked version of TinyGo](https://github.com/LimeChain/tinygo), we build the Runtime with target `polkawasm`, exported in `build/runtime.wasm`.

```bash
make build
```

**Run Tests**

After the runtime has been built, we execute standard Go tests with the help of a [forked version of Gossamer](https://github.com/LimeChain/gossamer), which we use
to import necessary Polkadot Host functionality and interact with the Runtime.

```bash
make test
```

**Optional steps**

* Inspect WASM Runtime - [wasmer](https://wasmer.io/)

```bash
wasmer inspect build/runtime.wasm
```

* Convert WASM from binary to text format - [wasm2wat](https://command-not-found.com/wasm2wat)

```bash
wasm2wat build/runtime.wasm -o build/runtime.wat
cat build/runtime.wat
```


## 4. Conclusion

The resulting proof of concept could be used to develop Polkadot Runtimes.
Also, current research document is useful for providing context as a starting point to make further contributions to the project or to take new direction for implementation of alternative toolchain.


## 5. Discussion

* How good is the performance?
* Is it possible in Wasm, to use separate heap regions, one reserved for GC allocations and another reserved for host allocations, to prevent clashes?
* Is LLVM the right choice for the long run or Binaryen is more suitable to provide stable foundation and support of Webassembly?
* How much effort will be to add full support of the `reflect` package?


## 6. References

* [1] https://docs.substrate.io/
* [2] https://github.com/w3f/polkadot-spec
* [3] https://github.com/paritytech/substrate
* [4] https://webassembly.org/
* [5] https://www.w3.org/TR/2019/REC-wasm-core-1-20191205
* [6] https://github.com/WebAssembly/design/blob/main/MVP.md
* [7] https://github.com/WebAssembly/spec
* [8] https://github.com/WebAssembly/proposals
* [9] https://github.com/WebAssembly/gc
* [10] https://github.com/WebAssembly/binaryen
* [11] https://llvm.org/
* [12] https://llvm.org/docs/GarbageCollection.html
* [13] https://go.dev/ref/spec
* [14] https://go.dev/doc/
* [15] https://go.dev/blog/
* [16] https://research.swtch.com/ 
* [17] https://github.com/golang/go
* [18] https://tinygo.org/
* [19] https://github.com/tinygo-org/tinygo
* [20] https://github.com/tinygo-org/tinygo/wiki
* [21] https://aykevl.nl/archive/