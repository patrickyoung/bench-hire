(async()=>{
  const checks=[],find=s=>document.querySelector(s),assert=(ok,message)=>{if(!ok)throw new Error(message);checks.push(message);};
  const wait=async(predicate,label)=>{for(let i=0;i<900;i++){if(predicate())return;await fetch('/__test/pulse');await new Promise(r=>setTimeout(r,20));}throw new Error('Timed out: '+label+' · '+document.querySelector('main')?.textContent.slice(-1600));};
  const visit=async(path,predicate)=>{history.pushState({},'',path);dispatchEvent(new PopStateEvent('popstate'));await wait(predicate,path);};
  const fill=(el,value)=>{el.value=value;el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));};
  const fit=label=>assert(document.documentElement.scrollWidth<=document.documentElement.clientWidth+1,label+' fits the viewport');
  try{
    await wait(()=>find('main'),'app');
    await visit('/workers/crm-assistant?tab=details',()=>find('a[href="/workers/crm-assistant/connections"]'));
    find('a[href="/workers/crm-assistant/connections"]').click();await wait(()=>find('.app-steps'),'connected apps');fit('App overview');
    find('a[href="/workers/crm-assistant/connections/new"]').click();await wait(()=>find('#app-import'),'setup');
    fill(find('#app-import'),JSON.stringify({mcpServers:{'Sales CRM':{url:'https://crm.example/mcp',headers:{Authorization:'Bearer service-secret'}}}}));find('[data-import]').click();
    assert(find('#app-name').value==='Sales CRM'&&find('#app-url').value==='https://crm.example/mcp','Service configuration fills the connection form');
    assert(!JSON.stringify(sessionStorage).includes('service-secret'),'Credentials are excluded from browser draft storage');fit('Connection setup');
    find('.app-connect-form').requestSubmit();await wait(()=>find('[data-form=app-permissions]'),'discovery completed');fit('Service capabilities');
    assert([...document.querySelectorAll('[data-permission]')].every(el=>el.value===''),'Discovery grants no employee permissions');
    assert(find('.app-capabilities').textContent.includes('email (required)'),'Required inputs are visible');
    const selects=[...document.querySelectorAll('[data-permission]')];fill(selects[0],'automatic');fill(selects[1],'review');fill(find('#app-intent'),'Find customers by exact email. Prepare new records for my review.');
    find('[data-form=app-permissions]').requestSubmit();await wait(()=>find('[data-form=app-teach] input[type=file]'),'saved access and teaching');
    const connectionPage=location.pathname;let form=find('[data-form=app-teach]');fill(form.elements.goal,'Use the CRM accurately, check duplicates and cite customer records.');
    const transfer=new DataTransfer();transfer.items.add(new File(['Use exact email matching. Check for duplicates before proposing a record.'],'crm-guide.md',{type:'text/markdown'}));const input=form.querySelector('input[type=file]');input.files=transfer.files;input.dispatchEvent(new Event('change',{bubbles:true}));
    await wait(()=>form.querySelector('[data-selected]')?.textContent.includes('Ready · source retained'),'guide ready');fit('Service teaching');
    form.requestSubmit();await wait(()=>find('[data-form=app-skill-install]'),'cited skill draft');
    assert(find('[data-app-skills] .prose a[href^="/sources/"]'),'The proposed service skill cites its retained materials');
    assert(!document.body.textContent.includes('service-secret'),'Credentials do not appear in catalogue or teaching output');
    form=find('[data-form=app-skill-install]');form.requestSubmit();await wait(()=>find('[data-app-skills] a[href*="/skills/"]'),'installed service skill');fit('Installed service skill');
    const skill=find('[data-app-skills] a[href*="/skills/"]');assert(skill.textContent==='Improve this skill','The service links directly to whole-folder skill improvement');
    skill.click();await wait(()=>find('[data-form=skill-improve]'),'skill improvement');assert(find('.skill-file-list').textContent.includes('references'),'Service skill resources remain part of the complete skill');
    await visit(connectionPage,()=>find('[data-revoke]'));find('[data-revoke]').click();await wait(()=>document.body.textContent.includes('access has been revoked'),'revocation');
    assert(!find('[data-form=app-teach]'),'Revoked access does not offer a new service training draft');
    find('[data-app-action=disconnect]').click();await wait(()=>find('[data-app-action=reconnect]'),'disconnection');fit('Disconnected service');
    assert(!JSON.stringify(sessionStorage).includes('service-secret'),'Credentials stay out of tab storage throughout the journey');
    await fetch('/__test/result',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({checks})});
  }catch(error){await fetch('/__test/result',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({error:String(error.stack||error),checks})});}
})();
