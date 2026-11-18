import {expect} from '@playwright/test';
import {test} from './fixtures.js';
const revision='c'.repeat(64),host='server-one';
async function connect(page){await page.goto('/');await page.getByLabel('Admin read token').fill('a'.repeat(64));await page.getByRole('button',{name:'Connect read-only'}).click();}
test('collection records partial progress without automatic write retry or readiness claim',async({page})=>{
 let posts=0,operation=null,unavailable=false;
 const recipe={id:'collect-server',revision,targets:[{host,binding_sequence:7,kind:'both'}]};
 await page.route('**/admin/api/v1/overhead-refresh',async route=>{
  if(route.request().method()==='POST'){
   posts++;const body=route.request().postDataJSON();expect(Object.keys(body).sort()).toEqual(['recipe','recipe_revision','request_id']);expect(body.request_id).toMatch(/^[a-f0-9]{32}$/);expect(route.request().headers().authorization).toBe('Bearer '+'b'.repeat(64));
   const pending={request_id:body.request_id,recipe:body.recipe,recipe_revision:body.recipe_revision,state:'pending',started_at:new Date().toISOString(),finished_at:'0001-01-01T00:00:00Z',results:[]};
   operation={...pending,state:'partial',finished_at:new Date().toISOString(),results:[{binding_sequence:7,artifact_sequence:9}]};
   return route.fulfill({status:202,json:pending});
  }
  if(unavailable)return route.fulfill({status:503});
  return route.fulfill({status:200,json:{recipes:[recipe],operations:operation?[operation]:[],running:false,can_start:true,blocked_reason:'',collection_enabled:true,accounting_only:true}});
 });
 await connect(page);
 await page.getByText('Collect authenticated native overhead',{exact:true}).click();
 await page.getByRole('button',{name:'Load collection recipes and receipts'}).click();
 await page.getByLabel('Approved collection recipe',{exact:true}).selectOption('collect-server');
 await page.getByLabel('Approve overhead accounting collection',{exact:true}).check();
 await page.getByLabel('Overhead collection management token',{exact:true}).fill('b'.repeat(64));
 await page.getByRole('button',{name:'Start new accounting collection',exact:true}).click();
 await expect(page.getByText('Partial collection receipt',{exact:true}).last()).toBeVisible();
 await expect(page.getByLabel('Overhead collection management token',{exact:true})).toHaveCount(0);
 expect(posts).toBe(1);
 unavailable=true;await page.getByRole('button',{name:'Load collection recipes and receipts'}).click();
 await expect(page.getByText('Collection status unavailable.',{exact:false})).toBeVisible();
 await expect(page.getByRole('button',{name:'Start new accounting collection',exact:true})).toHaveCount(0);
 expect(posts).toBe(1);
});
test('native enrollment keeps initial mode explicit and partial result requires fresh review',async({page})=>{
 let posts=0,gets=0;
 const recipe={id:'initial-server',revision,mode:'initial',hosts:[host]};
 await page.route('**/admin/api/v1/native-enrollment',async route=>{
  if(route.request().method()==='GET'){gets++;return route.fulfill({status:200,json:{recipes:[recipe],enrolled:posts?[{host,domain_id:'d'.repeat(64),sequence:10}]:[],ledger_sequence:posts?10:9,writes_enabled:true,readiness_authorized:false}});}
  posts++;const body=route.request().postDataJSON();expect(Object.keys(body).sort()).toEqual(['expected_sequence','recipe','recipe_revision']);expect(body.expected_sequence).toBe(9);
  return route.fulfill({status:503,json:{...body,confirmed_domains:0,partial:true,configuration_only:true,readiness_authorized:false,error:'observation unavailable'}});
 });
 await connect(page);
 await page.getByText('Enroll authenticated native domains',{exact:true}).click();
 await page.getByRole('button',{name:'Load native enrollment recipes and records'}).click();
 await page.getByLabel('Approved native enrollment recipe',{exact:true}).selectOption('initial-server');
 await expect(page.getByRole('alert').filter({hasText:'wholly unbound'})).toBeVisible();
 await page.getByLabel('Approve authenticated native enrollment',{exact:true}).check();
 await page.getByLabel('Native enrollment management token',{exact:true}).fill('b'.repeat(64));
 await page.getByRole('button',{name:'Record reviewed native enrollment',exact:true}).click();
 await expect(page.getByText('Enrollment did not fully confirm.',{exact:false})).toBeVisible();
 await expect(page.getByRole('button',{name:'Record reviewed native enrollment',exact:true})).toHaveCount(0);
 expect(posts).toBe(1);expect(gets).toBe(1);
 await page.getByRole('button',{name:'Load native enrollment recipes and records'}).click();
 await expect(page.getByText('server-one · sequence 10',{exact:false})).toBeVisible();
 await expect(page.getByLabel('Approved native enrollment recipe',{exact:true})).toHaveValue('');
 expect(posts).toBe(1);
});
