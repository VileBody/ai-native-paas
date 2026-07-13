#!/usr/bin/env python3
import json, os, signal, socket, subprocess, sys, time, urllib.error, urllib.request
from pathlib import Path

root=Path(__file__).resolve().parents[1]
binary=Path(sys.argv[1] if len(sys.argv)>1 else root/'bin/agent-api')
with socket.socket() as s:
    s.bind(('127.0.0.1',0)); port=s.getsockname()[1]
env=os.environ.copy();env.update({'AGENT_API_ADDR':f'127.0.0.1:{port}','AGENT_BOOTSTRAP_TENANT':'tenant-dev','AGENT_BOOTSTRAP_AGENT':'agent-dev','AGENT_BOOTSTRAP_USER':'user-dev','AGENT_BOOTSTRAP_TASK':'task-dev'})
log_path=root/'.verification/iteration-7/agent-api-server.log';log_path.parent.mkdir(parents=True,exist_ok=True)
log=open(log_path,'wb')
proc=subprocess.Popen([str(binary)],env=env,stdout=log,stderr=subprocess.STDOUT)
base=f'http://127.0.0.1:{port}'

def call(method,path,body=None,tenant=None,principal=None,kind=None,expected=None):
    data=None if body is None else json.dumps(body,separators=(',',':')).encode()
    req=urllib.request.Request(base+path,data=data,method=method,headers={'Content-Type':'application/json'})
    if tenant:
        req.add_header('X-Tenant-ID',tenant);req.add_header('X-Principal-ID',principal);req.add_header('X-Principal-Kind',kind)
    try:
        with urllib.request.urlopen(req,timeout=5) as resp:
            raw=resp.read();status=resp.status;headers=dict(resp.headers)
    except urllib.error.HTTPError as e:
        raw=e.read();status=e.code;headers=dict(e.headers)
    parsed=json.loads(raw or b'{}')
    if expected is not None and status!=expected: raise AssertionError(f'{method} {path}: {status} != {expected}: {parsed}')
    return status,parsed,headers

def invoke(tool,args,key,grant=''):
    body={'api_version':'agent.platform.example.com/v1','semantics_version':'v1','tenant_id':'tenant-dev','agent_id':'agent-dev','task_id':'task-dev','tool':tool,'arguments':args,'idempotency_key':key,'correlation_id':'corr-dev'}
    if grant: body['approval_grant_id']=grant
    status,out,_=call('POST','/mcp/v1/invoke',body,'tenant-dev','agent-dev','agent')
    if status not in (200,202): raise AssertionError((status,out))
    return out

checks={}
try:
    for _ in range(100):
        try:
            status,out,headers=call('GET','/healthz',expected=200);break
        except Exception:
            if proc.poll() is not None: raise RuntimeError(f'agent-api exited {proc.returncode}')
            time.sleep(.05)
    checks['health']=out
    _,catalog,headers=call('GET','/mcp/v1/tools',expected=200)
    assert catalog['api_version']=='agent.platform.example.com/v1' and len(catalog['tools'])==20 and headers.get('Cache-Control')=='no-store'
    checks['tool_catalog']=len(catalog['tools'])

    p1=invoke('platform_create_project',{'name':'booking'},'create-project')
    p2=invoke('platform_create_project',{'name':'booking'},'create-project')
    assert p1['result']['id']==p2['result']['id'] and p2['replayed'] is True
    checks['idempotent_project']=p1['result']['id']
    project_id=p1['result']['id']

    commit=invoke('platform_apply_repository_patch',{'project_id':project_id,'base_commit_sha':'abcdef0123456789','branch':'main','message':'initial service','files':[{'path':'main.go','content':'package main'}]},'commit-service')
    assert commit['result']['type']=='commit'
    commit_data=commit['result']['data']
    checks['repository_patch']=commit_data['commit_sha']

    build=invoke('platform_request_build',{'revision':{'project_id':project_id,'repository_id':commit_data['repository_id'],'branch':'main','commit_sha':commit_data['commit_sha']},'estimated_minutes':5},'build-service')
    build_id=build['result']['id'];checks['build']=build_id

    service=invoke('platform_provision_service',{'service_type':'postgres','plan':'small','name':'primary'},'provision-db')
    binding=invoke('platform_bind_service',{'service_instance_id':service['result']['id'],'application_id':'booking-api','environment_id':'env-production'},'bind-db')
    checks['managed_service']=binding['result']['id']

    secret_value='postgres://user:never-return@db/app'
    secret=invoke('platform_set_secret',{'application_id':'booking-api','environment_id':'env-production','name':'DATABASE_URL','value':secret_value},'set-secret')
    assert secret_value not in json.dumps(secret)
    metadata=invoke('platform_list_secret_metadata',{'application_id':'booking-api','environment_id':'env-production'},'list-secrets')
    assert secret_value not in json.dumps(metadata) and 'DATABASE_URL' in json.dumps(metadata)
    checks['write_only_secret']=True

    deploy_args={'build_id':build_id,'application_id':'booking-api','environment_id':'env-production','environment_name':'production','expected_environment_revision':1,'configuration':{'region':'eu1','isolation':'sandboxed','unit':'u1','processes':{'web':{'port':8080,'minReplicas':1,'maxReplicas':2,'healthPath':'/health'}},'generated_hostname':'booking.apps.example.test','attachment_snapshot_ref':'snapshot-1','rollout_timeout_seconds':300,'egress_profile':'public-default'}}
    approval=invoke('platform_request_approval',{'action':'deployment.production','resource':{'type':'environment','id':'env-production'},'payload':deploy_args,'ttl_seconds':600},'request-approval')
    approval_id=approval['result']['id']
    _,grant,_=call('POST',f'/v1/approval-requests/{approval_id}/grant',{},'tenant-dev','user-dev','user',201)
    deployed=invoke('platform_deploy',deploy_args,'deploy-production',grant['approval_grant_id'])
    checks['human_approved_deploy']=deployed['result']['id']
    status=invoke('platform_get_deployment',{'deployment_id':deployed['result']['id']},'deployment-status')
    assert status['result']['state']=='READY' and status['result']['url']
    checks['deployment_status']=status['result']['state']

    logs=invoke('platform_get_logs',{'application_id':'booking-api','limit':100},'logs')
    assert 'development-secret' not in json.dumps(logs) and '[REDACTED]' in json.dumps(logs)
    _,audit,_=call('GET','/v1/tasks/task-dev/audit',None,'tenant-dev','agent-dev','agent',200)
    raw=json.dumps(audit)
    assert secret_value not in raw and 'platform_deploy' in raw and 'platform_request_build' in raw
    checks['redacted_audit']=len(audit)

    body={'api_version':'agent.platform.example.com/v1','semantics_version':'v1','tenant_id':'tenant-dev','agent_id':'agent-dev','task_id':'task-dev','tool':'platform_create_project','arguments':{'name':'denied'},'idempotency_key':'cross','correlation_id':'corr-dev'}
    code,denied,_=call('POST','/mcp/v1/invoke',body,'tenant-other','agent-dev','agent',403)
    checks['cross_tenant_denied']=denied['error']['code']

    out_path=root/'.verification/iteration-7/agent-api-smoke.json'
    out_path.write_text(json.dumps(checks,indent=2,sort_keys=True)+'\n')
    print(json.dumps(checks,indent=2,sort_keys=True))
finally:
    proc.send_signal(signal.SIGTERM)
    try: proc.wait(timeout=5)
    except subprocess.TimeoutExpired: proc.kill();proc.wait()
    log.close()
