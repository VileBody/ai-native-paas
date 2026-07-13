#!/usr/bin/env python3
from pathlib import Path
import json

ROOT=Path(__file__).resolve().parents[1]
OUT=ROOT/'contracts/mcp/v1'
OUT.mkdir(parents=True,exist_ok=True)
BASE='https://platform.example.com/contracts/mcp/v1/'
DRAFT='https://json-schema.org/draft/2020-12/schema'

def obj(props,required=(),**kw):
    d={'type':'object','additionalProperties':False,'properties':props}
    if required:d['required']=list(required)
    d.update(kw);return d

def string(min=1,max=4096,pattern=None,enum=None,description=None):
    d={'type':'string','minLength':min,'maxLength':max}
    if pattern:d['pattern']=pattern
    if enum:d['enum']=enum
    if description:d['description']=description
    return d
ID=string(1,63,r'^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$')
SHA=string(7,64,r'^[0-9a-fA-F]{7,64}$')
BRANCH=string(1,255,r'^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$',description='A second server-side validator enforces the full safe Git ref subset.')
HOST=string(3,253,r'^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$')
COMMAND={'type':'array','maxItems':128,'items':string(0,4096)}
PROCESS=obj({'command':COMMAND,'port':{'type':'integer','minimum':0,'maximum':65535},'minReplicas':{'type':'integer','minimum':0,'maximum':1000},'maxReplicas':{'type':'integer','minimum':0,'maximum':1000},'healthPath':string(0,4096),'startupTimeoutSeconds':{'type':'integer','minimum':0,'maximum':86400},'readinessTimeoutSeconds':{'type':'integer','minimum':0,'maximum':86400}},('minReplicas','maxReplicas'))
MIGRATION=obj({'command':COMMAND,'timeoutSeconds':{'type':'integer','minimum':0,'maximum':86400}})
CONFIG=obj({'region':ID,'isolation':string(enum=['sandboxed','dedicated']),'unit':ID,'processes':{'type':'object','minProperties':1,'maxProperties':32,'propertyNames':{'pattern':r'^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$'},'additionalProperties':PROCESS},'generated_hostname':HOST,'attachment_snapshot_ref':ID,'migration':MIGRATION,'rollout_timeout_seconds':{'type':'integer','minimum':0,'maximum':86400},'egress_profile':string(0,64,enum=['','public-default','deny-all'])},('region','isolation','unit','processes'))
PATCH_FILE=obj({'path':string(1,4096),'content':string(0,1048576)},('path','content'))
REVISION=obj({'project_id':ID,'repository_id':ID,'branch':BRANCH,'commit_sha':SHA,'source_root':string(0,4096)},('project_id','repository_id','branch','commit_sha'))
RESOURCE=obj({'type':ID,'id':ID},('type','id'))
ACTIONS=['application.delete.production','service.purge.database','domain.transfer','commercial.increase_paid_limits','deployment.production','service.disable_backups','network.expose_smtp']
SCHEMAS={
'platform_create_project':obj({'name':string(1,100)},('name',)),
'platform_get_project':obj({'project_id':ID},('project_id',)),
'platform_apply_repository_patch':obj({'project_id':ID,'base_commit_sha':SHA,'branch':BRANCH,'message':string(1,500),'files':{'type':'array','minItems':1,'maxItems':1000,'items':PATCH_FILE}},('project_id','base_commit_sha','branch','message','files')),
'platform_create_branch':obj({'project_id':ID,'branch':BRANCH,'base_commit_sha':SHA},('project_id','branch','base_commit_sha')),
'platform_create_merge_request':obj({'project_id':ID,'source_branch':BRANCH,'target_branch':BRANCH,'title':string(1,500)},('project_id','source_branch','target_branch','title')),
'platform_request_build':obj({'revision':REVISION,'estimated_minutes':{'type':'integer','minimum':0,'maximum':1440}},('revision',)),
'platform_get_build':obj({'build_id':ID},('build_id',)),
'platform_deploy':obj({'build_id':ID,'application_id':ID,'environment_id':ID,'environment_name':ID,'expected_environment_revision':{'type':'integer','minimum':0},'configuration':CONFIG},('build_id','application_id','environment_id','environment_name','expected_environment_revision','configuration')),
'platform_get_deployment':obj({'deployment_id':ID},('deployment_id',)),
'platform_rollback':obj({'deployment_id':ID,'target_release_id':ID,'expected_environment_revision':{'type':'integer','minimum':0}},('deployment_id','target_release_id','expected_environment_revision')),
'platform_set_secret':obj({'application_id':ID,'environment_id':ID,'name':string(1,128,r'^[A-Z][A-Z0-9_]{0,127}$'),'value':string(1,65536)},('application_id','environment_id','name','value')),
'platform_list_secret_metadata':obj({'application_id':ID,'environment_id':ID},('application_id','environment_id')),
'platform_provision_service':obj({'service_type':ID,'plan':ID,'name':ID},('service_type','plan','name')),
'platform_bind_service':obj({'service_instance_id':ID,'application_id':ID,'environment_id':ID},('service_instance_id','application_id','environment_id')),
'platform_add_domain':obj({'application_id':ID,'environment_id':ID,'hostname':HOST},('application_id','environment_id','hostname')),
'platform_get_logs':obj({'application_id':ID,'limit':{'type':'integer','minimum':1,'maximum':10000}},('application_id','limit')),
'platform_get_usage':obj({'period_id':ID},('period_id',)),
'platform_request_approval':obj({'action':string(enum=ACTIONS),'resource':RESOURCE,'payload':{},'ttl_seconds':{'type':'integer','minimum':1,'maximum':86400}},('action','resource','payload','ttl_seconds')),
'platform_get_operation':obj({'operation_id':ID},('operation_id',)),
'platform_cancel_operation':obj({'operation_id':ID},('operation_id',)),
}
for name,schema in sorted(SCHEMAS.items()):
    schema={'$schema':DRAFT,'$id':BASE+name+'.schema.json','title':name+' arguments',**schema}
    (OUT/(name+'.schema.json')).write_text(json.dumps(schema,indent=2,sort_keys=True)+'\n')

tools=[]
for name in sorted(SCHEMAS):
    tools.append({'name':name,'inputSchema':BASE+name+'.schema.json','semanticsVersion':'v1'})
(OUT/'catalog.json').write_text(json.dumps({'api_version':'agent.platform.example.com/v1','semantics_version':'v1','tools':tools},indent=2,sort_keys=True)+'\n')
(OUT/'tools.json').write_text(json.dumps(tools,indent=2,sort_keys=True)+'\n')

inv=obj({'api_version':{'const':'agent.platform.example.com/v1'},'semantics_version':{'const':'v1'},'tenant_id':ID,'agent_id':ID,'task_id':ID,'tool':{'type':'string','enum':sorted(SCHEMAS)},'arguments':{},'idempotency_key':string(1,128),'correlation_id':ID,'approval_grant_id':ID},('api_version','semantics_version','tenant_id','agent_id','task_id','tool','arguments','idempotency_key','correlation_id'))
inv={'$schema':DRAFT,'$id':BASE+'invocation-request.schema.json','title':'Agent invocation request',**inv}
(OUT/'invocation-request.schema.json').write_text(json.dumps(inv,indent=2,sort_keys=True)+'\n')
operation=obj({'operation_id':ID,'tenant_id':ID,'state':string(enum=['PENDING','RUNNING','WAITING_EXTERNAL','SUCCEEDED','FAILED','CANCELED'])},('operation_id','state'))
result=obj({'type':ID,'id':ID,'url':{'type':'string','maxLength':4096},'state':{'type':'string','maxLength':128},'data':{}},('type','id'))
error=obj({'code':string(1,64),'message':string(1,1024),'operation_id':ID,'retryable':{'type':'boolean'},'details':{'type':'object'}},('code','message','retryable'))
resp=obj({'api_version':{'const':'agent.platform.example.com/v1'},'invocation_id':ID,'operation':operation,'result':result,'error':error,'replayed':{'type':'boolean'}},('api_version','invocation_id','replayed'))
resp['oneOf']=[{'required':['operation'],'not':{'required':['result']}},{'required':['result'],'not':{'required':['operation']}}]
resp={'$schema':DRAFT,'$id':BASE+'invocation-response.schema.json','title':'Agent invocation response',**resp}
(OUT/'invocation-response.schema.json').write_text(json.dumps(resp,indent=2,sort_keys=True)+'\n')
(OUT/'public-error.schema.json').write_text(json.dumps({'$schema':DRAFT,'$id':BASE+'public-error.schema.json','title':'Public error',**error},indent=2,sort_keys=True)+'\n')

openapi={'openapi':'3.1.0','info':{'title':'AI-native PaaS Agent API','version':'1.0.0'},'paths':{'/healthz':{'get':{'responses':{'200':{'description':'healthy'}}}},'/mcp/v1/tools':{'get':{'responses':{'200':{'description':'tool catalog'}}}},'/mcp/v1/invoke':{'post':{'requestBody':{'required':True,'content':{'application/json':{'schema':{'$ref':'../mcp/v1/invocation-request.schema.json'}}}},'responses':{'200':{'description':'synchronous result'},'202':{'description':'asynchronous operation'},'4XX':{'description':'stable public error'}}}},'/v1/tasks/{task_id}/status':{'get':{'parameters':[{'name':'task_id','in':'path','required':True,'schema':ID}],'responses':{'200':{'description':'task status'}}}},'/v1/tasks/{task_id}/audit':{'get':{'parameters':[{'name':'task_id','in':'path','required':True,'schema':ID}],'responses':{'200':{'description':'redacted audit'}}}},'/v1/tasks/{task_id}/resume':{'post':{'parameters':[{'name':'task_id','in':'path','required':True,'schema':ID}],'responses':{'200':{'description':'resumed'}}}},'/v1/approval-requests/{request_id}/grant':{'post':{'parameters':[{'name':'request_id','in':'path','required':True,'schema':ID}],'responses':{'201':{'description':'grant'}}}},'/v1/approval-requests/{request_id}/deny':{'post':{'parameters':[{'name':'request_id','in':'path','required':True,'schema':ID}],'responses':{'200':{'description':'denied'}}}}}}
op=ROOT/'contracts/openapi';op.mkdir(parents=True,exist_ok=True);(op/'agent-v1.openapi.json').write_text(json.dumps(openapi,indent=2,sort_keys=True)+'\n')
