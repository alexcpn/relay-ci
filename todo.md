  Pipeline tamper protection (TOFU digest):                                                                                                                                                   
  - Project identity = SHA of the repo's root commit (git rev-list --max-parents=0 HEAD). Stable, deterministic, no user config needed.                                                       
  - Master keeps <data-root>/pipeline-digests.json: { root_commit_sha → { digest, first_seen, last_seen, pipeline_path } }.                                                                   
  - First verify run: compute sha256(pipeline.yaml from bundle), store, proceed. CLI prints pinned pipeline digest abc123....                                                                 
  - Subsequent runs:                                                                                                                                                                          
    - Digest matches → run.                                                                                                                                                                   
    - Digest differs → fail before scheduling, exit code 2, print:                                                                                                                            
    PIPELINE CHANGED — refusing to run                                                                                                                                                        
  pinned:  sha256:abc123...  (first seen 2026-04-01)                                                                                                                                          
  current: sha256:def456...                                                                                                                                                                   
  diff:                                                                                                                                                                                       
    <unified diff of pipeline.yaml old → new>
  To accept: ci-cli verify --accept-pipeline-change <path> ...                                                                                                                                
    - With --accept-pipeline-change: print the diff, write new digest, re-pin, run.                                                                                                           
  - Exit codes: 0 pass, 1 build/review fail, 2 pipeline tamper, 3 infra/CLI error.                                                                                                            
                                                                                                                                                                                              
  Operator override: ci-cli pipeline-pin list | unpin <root-sha> for manual cleanup.                                                                                                          
                                                                                                                                                                                              
  Open Q on Wails specifics: the project is a Wails+Go app — anything in the verify pipeline you want pre-baked into a starter pipeline.yaml? I'm thinking: go vet, golangci-lint, go test    
  -race -cover (with min coverage gate), wails build (smoke check the bundle compiles), then code_review. I can drop a examples/wails-pipeline.yaml so the AI agent can copy it on day one.   
  Want that included?                                                                                                                                                                         
                  
  Once you confirm the Wails pipeline question, I'll implement in this order:                                                                                                                 
  1. Proto: VerifyLocal streaming RPC + VerifyLocalResponse with verdict, code_review_text, task_results[].
  2. Master: bundle receiver, digest pinning, pipeline parse from bundle, submit build, wait-for-terminal, gather review output.                                                              
  3. Dispatcher/worker: bind-mount file:// bundles into the clone container.                                                    
  4. CLI: verify subcommand (blocking) and pipeline-pin subcommand.                                                                                                                           
  5. examples/wails-pipeline.yaml (if you want it).                                                                                                                                           
  6. QuickStart section.                                                                                                                                                                      
                                                                                                                                                                                              
❯ yes pls example go and wails pipleline that I can copy also  
  6 tasks (1 done, 1 in progress, 4 open)
  ◼ Implement master verify handler with bundle + digest pinning
  ◻ Bind-mount file:// bundles into clone container
  ◻ Add ci-cli verify and pipeline-pin commands
  ◻ Add example Go and Wails verify pipelines
  ◻ Update QuickStart.md with verify section
   … +1 completed



